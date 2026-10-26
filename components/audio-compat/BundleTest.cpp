#include <CoreAudio/AudioServerPlugIn.h>
#include <CoreFoundation/CoreFoundation.h>
#include <cassert>
#include <cstdint>
#include <cstring>
#include <dlfcn.h>
#include <iostream>
#include <unistd.h>

static OSStatus changed(AudioServerPlugInHostRef, AudioObjectID, UInt32, const AudioObjectPropertyAddress*) { return noErr; }
static OSStatus readStorage(AudioServerPlugInHostRef, CFStringRef, CFPropertyListRef* out) { *out=nullptr; return noErr; }
static OSStatus writeStorage(AudioServerPlugInHostRef, CFStringRef, CFPropertyListRef) { return noErr; }
static OSStatus deleteStorage(AudioServerPlugInHostRef, CFStringRef) { return noErr; }
static OSStatus config(AudioServerPlugInHostRef, AudioObjectID, UInt64, void*) { return kAudioHardwareUnsupportedOperationError; }

template<class T> T property(AudioServerPlugInDriverRef driver, AudioObjectID object,
                            AudioObjectPropertySelector selector, AudioObjectPropertyScope scope=kAudioObjectPropertyScopeGlobal) {
    AudioObjectPropertyAddress address{selector,scope,kAudioObjectPropertyElementMain};
    UInt32 size=0; T value{};
    assert((*driver)->GetPropertyDataSize(driver,object,getpid(),&address,0,nullptr,&size)==noErr);
    assert(size==sizeof(T));
    assert((*driver)->GetPropertyData(driver,object,getpid(),&address,0,nullptr,sizeof(T),&size,&value)==noErr);
    assert(size==sizeof(T)); return value;
}
int main(int argc,char** argv) {
    assert(argc==2);
    void* library=dlopen(argv[1],RTLD_NOW|RTLD_LOCAL);
    if(!library) { std::cerr<<dlerror()<<"\n"; return 1; }
    auto factory=reinterpret_cast<void*(*)(CFAllocatorRef,CFUUIDRef)>(dlsym(library,"MiMacAudioFactory"));
    assert(factory && factory(nullptr,nullptr)==nullptr);
    auto driver=static_cast<AudioServerPlugInDriverRef>(factory(nullptr,kAudioServerPlugInTypeUUID));
    assert(driver);
    AudioServerPlugInHostInterface host{};
    host.PropertiesChanged=changed;host.CopyFromStorage=readStorage;host.WriteToStorage=writeStorage;
    host.DeleteFromStorage=deleteStorage;host.RequestDeviceConfigurationChange=config;
    assert((*driver)->Initialize(driver,&host)==noErr);
    const auto device=property<AudioObjectID>(driver,kAudioObjectPlugInObject,kAudioPlugInPropertyDeviceList);
    auto uid=property<CFStringRef>(driver,device,kAudioDevicePropertyDeviceUID);
    assert(CFEqual(uid,CFSTR("dev.mimac.audio.compat.v1")));CFRelease(uid);
    assert(property<Float64>(driver,device,kAudioDevicePropertyNominalSampleRate)==16000);
    // AudioServerPlugIn.h requires at least 10923 frames, regardless of the I/O buffer size.
    assert(property<UInt32>(driver,device,kAudioDevicePropertyZeroTimeStampPeriod)>=10923);
    for(auto scope:{kAudioObjectPropertyScopeInput,kAudioObjectPropertyScopeOutput}) {
        assert(property<UInt32>(driver,device,kAudioDevicePropertyDeviceCanBeDefaultDevice,scope)==0);
        assert(property<UInt32>(driver,device,kAudioDevicePropertyDeviceCanBeDefaultSystemDevice,scope)==0);
        auto stream=property<AudioObjectID>(driver,device,kAudioDevicePropertyStreams,scope);
        auto f=property<AudioStreamBasicDescription>(driver,stream,kAudioStreamPropertyPhysicalFormat);
        assert(f.mSampleRate==16000 && f.mChannelsPerFrame==1 && f.mBitsPerChannel==16 && f.mBytesPerFrame==2);
    }
    const auto input=property<AudioObjectID>(driver,device,kAudioDevicePropertyStreams,kAudioObjectPropertyScopeInput);
    const auto output=property<AudioObjectID>(driver,device,kAudioDevicePropertyStreams,kAudioObjectPropertyScopeOutput);
    AudioServerPlugInClientInfo client{7,getpid(),true,CFSTR("dev.mimac.driver.test")};
    assert((*driver)->AddDeviceClient(driver,device,&client)==noErr);
    for(auto operation:{kAudioServerPlugInIOOperationReadInput,kAudioServerPlugInIOOperationWriteMix}) {
        Boolean yes=false,inplace=false;
        assert((*driver)->WillDoIOOperation(driver,device,7,operation,&yes,&inplace)==noErr && yes && inplace);
    }
    assert((*driver)->StartIO(driver,device,7)==noErr);
    Float64 sample=0;UInt64 time=0,seed=0;
    assert((*driver)->GetZeroTimeStamp(driver,device,7,&sample,&time,&seed)==noErr && time>0);
    AudioServerPlugInIOCycleInfo cycle{};
    cycle.mInputTime.mSampleTime=cycle.mOutputTime.mSampleTime=128;
    cycle.mInputTime.mFlags=cycle.mOutputTime.mFlags=kAudioTimeStampSampleTimeValid;
    int16_t sent[128],received[128];
    for(int i=0;i<128;++i) sent[i]=static_cast<int16_t>(i*127-8000);
    auto io=[&](UInt32 op,AudioObjectID stream,void* buffer) {
        assert((*driver)->DoIOOperation(driver,device,stream,7,op,128,&cycle,buffer,nullptr)==noErr);
    };
    io(kAudioServerPlugInIOOperationReadInput,input,received);
    for(auto s:received) assert(s==0);
    io(kAudioServerPlugInIOOperationWriteMix,output,sent);
    io(kAudioServerPlugInIOOperationReadInput,input,received);
    assert(std::memcmp(sent,received,sizeof(sent))==0);
    // The final stop and next first start must erase the prior session.
    assert((*driver)->StopIO(driver,device,7)==noErr);
    assert((*driver)->StartIO(driver,device,7)==noErr);
    io(kAudioServerPlugInIOOperationReadInput,input,received);
    for(auto s:received) assert(s==0);
    assert((*driver)->StopIO(driver,device,7)==noErr);
    assert((*driver)->RemoveDeviceClient(driver,device,&client)==noErr);
    std::cout<<"Bundle ABI checks passed: UID, formats, no-default, native I/O, clock, reset\n";
    // Keep the plug-in loaded until process exit, as the real HAL host does.
}
