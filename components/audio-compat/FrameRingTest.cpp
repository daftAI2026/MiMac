#include "FrameRing.hpp"

#include <atomic>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <limits>
#include <thread>
#include <vector>

namespace {

void require(bool condition, const char* message) {
  if (!condition) {
    std::fprintf(stderr, "FAIL: %s\n", message);
    std::exit(1);
  }
}

void testZeroTimestampAndExactBlock() {
  FrameRing ring;
  const std::int16_t source[] = {0, -1, 1234, -32768, 32767};
  std::int16_t result[] = {9, 9, 9, 9, 9};

  ring.write(0, source, 5);
  ring.read(0, result, 5);
  for (std::size_t i = 0; i < 5; ++i) {
    require(result[i] == source[i], "frame zero and exact block round-trip");
  }
}

void testGapsAndCapacityWrap() {
  FrameRing ring;
  const std::int16_t adjacent[] = {41, -42};
  ring.write(100, adjacent, 2);

  std::int16_t withGaps[] = {7, 7, 7, 7};
  ring.read(99, withGaps, 4);
  require(withGaps[0] == 0 && withGaps[1] == 41 &&
              withGaps[2] == -42 && withGaps[3] == 0,
          "unwritten gaps return zero around valid frames");

  const std::int64_t start = static_cast<std::int64_t>(FrameRing::kCapacity - 2);
  const std::int16_t crossing[] = {11, 22, 33, 44};
  std::int16_t wrapped[4] = {};
  ring.write(start, crossing, 4);
  ring.read(start, wrapped, 4);
  for (std::size_t i = 0; i < 4; ++i) {
    require(wrapped[i] == crossing[i],
            "slot index wraps across the power-of-two boundary");
  }
}

void testOverwrittenCapacityReturnsZero() {
  FrameRing ring;
  std::vector<std::int16_t> first(FrameRing::kCapacity);
  std::vector<std::int16_t> second(FrameRing::kCapacity);
  for (std::size_t i = 0; i < FrameRing::kCapacity; ++i) {
    first[i] = static_cast<std::int16_t>(i + 1);
    second[i] = static_cast<std::int16_t>(-static_cast<int>(i + 1));
  }

  ring.write(0, first.data(), first.size());
  ring.write(static_cast<std::int64_t>(FrameRing::kCapacity), second.data(),
             second.size());

  std::int16_t stale = 99;
  std::int16_t current = 0;
  ring.read(0, &stale, 1);
  ring.read(static_cast<std::int64_t>(FrameRing::kCapacity), &current, 1);
  require(stale == 0, "capacity-overwritten frame returns zero");
  require(current == second[0], "replacement frame matches its timestamp");
}

void testInvalidTimestampsAndBounds() {
  FrameRing ring;
  const std::int16_t payload[] = {71, 72};
  std::int16_t result[] = {9, 9};

  std::int64_t converted = 123;
  require(!FrameRing::tryConvertTimestamp(-1.0, converted),
          "negative sample-time is rejected");
  require(converted == 123, "failed timestamp conversion leaves output alone");
  require(!FrameRing::tryConvertTimestamp(std::numeric_limits<double>::quiet_NaN(),
                                          converted),
          "NaN sample-time is rejected");
  require(!FrameRing::tryConvertTimestamp(
              std::numeric_limits<double>::infinity(), converted),
          "infinite sample-time is rejected");
  require(!FrameRing::tryConvertTimestamp(12.5, converted),
          "fractional sample-time is rejected");
  require(!FrameRing::tryConvertTimestamp(
              static_cast<double>(FrameRing::kTimestampLimit), converted),
          "sample-time at the exclusive 48-bit bound is rejected");

  ring.write(-1, payload, 2);
  ring.read(-1, result, 2);
  require(result[0] == 0 && result[1] == 0,
          "negative read timestamp returns a zero block");

  const auto lastValid = static_cast<std::int64_t>(FrameRing::kTimestampLimit - 1);
  const std::int16_t lastSample = -712;
  ring.write(lastValid, &lastSample, 1);
  std::int16_t atLimit = 9;
  ring.read(lastValid, &atLimit, 1);
  require(atLimit == lastSample, "last valid 48-bit frame is addressable");

  const std::int16_t crossing[] = {801, 802};
  ring.write(lastValid, crossing, 2);
  std::int16_t stillLast = 0;
  ring.read(lastValid, &stillLast, 1);
  require(stillLast == lastSample,
          "out-of-range write is rejected as a whole block");

  result[0] = 9;
  result[1] = 9;
  ring.read(lastValid, result, 2);
  require(result[0] == 0 && result[1] == 0,
          "out-of-range read returns a zero block");

  const auto maxInt64 = std::numeric_limits<std::int64_t>::max();
  ring.write(maxInt64, payload, 1);
  result[0] = 9;
  ring.read(maxInt64, result, 1);
  require(result[0] == 0, "extreme signed timestamp is rejected safely");
}

void testMultipleReadersDuringWriter() {
  FrameRing ring;
  constexpr std::size_t kFrameCount = 30000;
  constexpr std::size_t kReaderCount = 3;

  std::atomic<bool> begin{false};
  std::atomic<bool> writerDone{false};
  std::atomic<std::size_t> ready{0};
  std::atomic<std::uint64_t> checks{0};
  std::atomic<std::uint64_t> wrongSamples{0};
  std::vector<std::thread> readers;
  readers.reserve(kReaderCount);

  for (std::size_t reader = 0; reader < kReaderCount; ++reader) {
    readers.emplace_back([&, reader] {
      ready.fetch_add(1, std::memory_order_release);
      while (!begin.load(std::memory_order_acquire)) {
        std::this_thread::yield();
      }

      std::uint64_t iteration = reader * 127;
      do {
        for (std::size_t batch = 0; batch < 512; ++batch, ++iteration) {
          const std::uint64_t frame =
              (iteration * 7919 + reader * 101) % kFrameCount;
          std::int16_t sample = 0;
          ring.read(static_cast<std::int64_t>(frame), &sample, 1);
          const auto expected = static_cast<std::int16_t>(frame + 1);
          if (sample != 0 && sample != expected) {
            wrongSamples.fetch_add(1, std::memory_order_relaxed);
          }
          checks.fetch_add(1, std::memory_order_relaxed);
        }
      } while (!writerDone.load(std::memory_order_acquire));
    });
  }

  while (ready.load(std::memory_order_acquire) != kReaderCount) {
    std::this_thread::yield();
  }

  std::thread writer([&] {
    begin.store(true, std::memory_order_release);
    for (std::size_t frame = 0; frame < kFrameCount; ++frame) {
      const auto sample = static_cast<std::int16_t>(frame + 1);
      ring.write(static_cast<std::int64_t>(frame), &sample, 1);
      if ((frame & 63U) == 0) {
        std::this_thread::yield();
      }
    }
    writerDone.store(true, std::memory_order_release);
  });

  writer.join();
  for (auto& reader : readers) {
    reader.join();
  }

  require(checks.load(std::memory_order_relaxed) >= kReaderCount * 512,
          "all concurrent readers completed checks");
  require(wrongSamples.load(std::memory_order_relaxed) == 0,
          "readers see only matching samples or zero during overwrite");
}

}  // namespace

int main() {
  testZeroTimestampAndExactBlock();
  testGapsAndCapacityWrap();
  testOverwrittenCapacityReturnsZero();
  testInvalidTimestampsAndBounds();
  testMultipleReadersDuringWriter();
  std::puts("FrameRing tests passed");
  return 0;
}
