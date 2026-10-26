// MiMac optional audio endpoint. The HAL object/clock/property implementation
// is supplied by libASPL; this adapter only transfers bounded timestamped PCM.
#include "FrameRing.hpp"
#include <aspl/Driver.hpp>
#include <aspl/Tracer.hpp>
#include <cmath>
#include <cstring>
#include <memory>

namespace mimac {
constexpr UInt32 kSampleRate = 16000;
constexpr const char* kDeviceUID = "dev.mimac.audio.compat.v1";

class Loopback final : public aspl::ControlRequestHandler, public aspl::IORequestHandler {
public:
    OSStatus OnStartIO() override { ring_.clear(); return noErr; }
    void OnStopIO() override { ring_.clear(); }
    void OnWriteMixedOutput(const std::shared_ptr<aspl::Stream>&, Float64, Float64 timestamp,
                            const void* bytes, UInt32 count) override {
        if (!bytes || count % sizeof(int16_t) || !validTime(timestamp, count / sizeof(int16_t))) return;
        ring_.write(static_cast<int64_t>(timestamp), static_cast<const int16_t*>(bytes), count / sizeof(int16_t));
    }
    void OnReadClientInput(const std::shared_ptr<aspl::Client>&, const std::shared_ptr<aspl::Stream>&,
                           Float64, Float64 timestamp, void* bytes, UInt32 count) override {
        if (!bytes) return;
        if (count % sizeof(int16_t) || !validTime(timestamp, count / sizeof(int16_t))) {
            std::memset(bytes, 0, count); return;
        }
        ring_.read(static_cast<int64_t>(timestamp), static_cast<int16_t*>(bytes), count / sizeof(int16_t));
    }
private:
    static bool validTime(double t, size_t frames) {
        constexpr uint64_t limit = (uint64_t{1} << 48) - 2;
        return std::isfinite(t) && t >= 0 && std::floor(t) == t && t <= double(limit) &&
            frames <= FrameRing::kCapacity && frames <= limit - static_cast<uint64_t>(t);
    }
    FrameRing ring_;
};

std::shared_ptr<aspl::Driver> MakeDriver() {
    auto context = std::make_shared<aspl::Context>(std::make_shared<aspl::Tracer>(aspl::Tracer::Mode::Noop));
    aspl::DeviceParameters parameters;
    parameters.Name = "MiMac Input";
    parameters.Manufacturer = "MiMac";
    parameters.DeviceUID = kDeviceUID;
    parameters.ModelUID = "dev.mimac.audio.compat";
    parameters.ConfigurationApplicationBundleID = "dev.mimac.app";
    parameters.SampleRate = kSampleRate;
    parameters.ChannelCount = 1;
    // Use libASPL's one-second clock period (16000 frames). This property is
    // independent of I/O block size and Apple requires at least 10923 frames.
    parameters.CanBeDefault = false;
    parameters.CanBeDefaultForSystemSounds = false;
    parameters.EnableMixing = true;
    parameters.EnableRealtimeTracing = false;
    auto device = std::make_shared<aspl::Device>(context, parameters);
    // No volume/mute controls: unity gain, no persisted hidden mute state.
    device->AddStreamAsync(aspl::Direction::Input);
    device->AddStreamAsync(aspl::Direction::Output);
    auto handler = std::make_shared<Loopback>();
    device->SetControlHandler(handler);
    device->SetIOHandler(handler);
    auto plugin = std::make_shared<aspl::Plugin>(context);
    plugin->AddDevice(device);
    return std::make_shared<aspl::Driver>(context, plugin);
}
}
extern "C" __attribute__((visibility("default"))) void* MiMacAudioFactory(CFAllocatorRef, CFUUIDRef type) {
    if (!type || !CFEqual(type, kAudioServerPlugInTypeUUID)) return nullptr;
    static auto driver = mimac::MakeDriver();
    return driver->GetReference();
}
