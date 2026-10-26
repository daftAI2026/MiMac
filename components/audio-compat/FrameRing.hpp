#ifndef MIMAC_PROTOTYPE_FRAME_RING_HPP
#define MIMAC_PROTOTYPE_FRAME_RING_HPP

#include <atomic>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <limits>

// A single-writer, multi-reader ring for one mono SInt16 stream at 16 kHz.
// Each atomic slot contains a 48-bit (frame + 1) tag and a 16-bit sample.
// Tag zero marks an empty slot, so frame zero is represented unambiguously.
//
// The caller must validate the CoreAudio timestamp flags and format. The
// helper below validates a sample-time value once it has been extracted:
// timestamps must be finite, nonnegative, integral, and below 2^48 - 1.
// Invalid writes are dropped as a whole; invalid reads return a zero block.
//
// write() has exactly one audio-thread writer. Any number of input readers may
// call read(). Neither path allocates, locks, or logs. clear() is only valid
// while I/O is quiescent (for example, in OnStartIO/OnStopIO).
class FrameRing final {
 public:
  static constexpr std::size_t kCapacity = 8192;
  static constexpr std::uint64_t kTimestampLimit = (std::uint64_t{1} << 48) - 1;

  static_assert((kCapacity & (kCapacity - 1)) == 0,
                "FrameRing capacity must be a power of two");
  static_assert(std::atomic<std::uint64_t>::is_always_lock_free,
                "FrameRing requires lock-free 64-bit atomics");

  FrameRing() noexcept { clear(); }
  FrameRing(const FrameRing&) = delete;
  FrameRing& operator=(const FrameRing&) = delete;

  // Convert an extracted CoreAudio sample-time to the frame-index API.
  // The caller still owns validation of AudioTimeStamp flags / sample-time
  // presence before calling this helper.
  static bool tryConvertTimestamp(double sampleTime,
                                  std::int64_t& frame) noexcept {
    if (!std::isfinite(sampleTime) || sampleTime < 0.0 ||
        std::trunc(sampleTime) != sampleTime ||
        sampleTime >= static_cast<double>(kTimestampLimit)) {
      return false;
    }
    frame = static_cast<std::int64_t>(sampleTime);
    return true;
  }

  // Clear only while the I/O callbacks are quiescent. This is intentionally
  // a bounded loop over preallocated atomics.
  void clear() noexcept {
    for (std::size_t i = 0; i < kCapacity; ++i) {
      slots_[i].store(0, std::memory_order_relaxed);
    }
  }

  // Publish a contiguous block. A bad timestamp or a block crossing the
  // representable frame range drops the entire block without partial writes.
  void write(std::int64_t start, const std::int16_t* samples,
             std::size_t count) noexcept {
    if (samples == nullptr || !validBlock(start, count)) {
      return;
    }

    const auto first = static_cast<std::uint64_t>(start);
    for (std::size_t i = 0; i < count; ++i) {
      const std::uint64_t frame = first + static_cast<std::uint64_t>(i);
      const std::uint64_t tag = frame + 1;
      const std::uint64_t payload =
          (tag << 16) | static_cast<std::uint16_t>(samples[i]);
      slots_[frame & (kCapacity - 1)].store(payload,
                                             std::memory_order_relaxed);
    }
  }

  // Copy a contiguous block. A missing, stale, or mismatched frame is zero.
  // Invalid timestamps / ranges zero the entire requested destination block.
  void read(std::int64_t start, std::int16_t* samples,
            std::size_t count) const noexcept {
    if (samples == nullptr) {
      return;
    }
    if (!validBlock(start, count)) {
      zero(samples, count);
      return;
    }

    const auto first = static_cast<std::uint64_t>(start);
    for (std::size_t i = 0; i < count; ++i) {
      const std::uint64_t frame = first + static_cast<std::uint64_t>(i);
      const std::uint64_t packed =
          slots_[frame & (kCapacity - 1)].load(std::memory_order_relaxed);
      if ((packed >> 16) == frame + 1) {
        samples[i] = decodeSample(static_cast<std::uint16_t>(packed));
      } else {
        samples[i] = 0;
      }
    }
  }

 private:
  static bool validBlock(std::int64_t start, std::size_t count) noexcept {
    if (start < 0) {
      return false;
    }
    const std::uint64_t first = static_cast<std::uint64_t>(start);
    if (first >= kTimestampLimit) {
      return false;
    }
    const std::uint64_t available = kTimestampLimit - first;
    return count <= available;
  }

  static void zero(std::int16_t* samples, std::size_t count) noexcept {
    for (std::size_t i = 0; i < count; ++i) {
      samples[i] = 0;
    }
  }

  static std::int16_t decodeSample(std::uint16_t bits) noexcept {
    const std::int32_t value =
        bits <= static_cast<std::uint16_t>(std::numeric_limits<std::int16_t>::max())
            ? static_cast<std::int32_t>(bits)
            : static_cast<std::int32_t>(bits) - 0x10000;
    return static_cast<std::int16_t>(value);
  }

  std::atomic<std::uint64_t> slots_[kCapacity];
};

#endif  // MIMAC_PROTOTYPE_FRAME_RING_HPP
