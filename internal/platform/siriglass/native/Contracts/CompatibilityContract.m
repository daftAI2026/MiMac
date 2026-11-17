#define dlopen fixture_dlopen
#define dlsym fixture_dlsym
#define dlerror fixture_dlerror
#ifndef MIMAC_COMPATIBILITY_SOURCE
#define MIMAC_COMPATIBILITY_SOURCE "../Sources/CGlassCompatibility/Compatibility.m"
#endif
#include MIMAC_COMPATIBILITY_SOURCE
#undef dlopen
#undef dlsym
#undef dlerror

static uint64_t glassWitness[12], meterWitness[12], optionWitness[12];
static void *glassMetadata[2], *meterMetadata[2], *optionMetadata[2];
static GlassMetadataResponse fixture_glass(uint64_t request) { return (GlassMetadataResponse){ &glassMetadata[1], 0 }; }
static GlassMetadataResponse fixture_meters(uint64_t request) { return (GlassMetadataResponse){ &meterMetadata[1], 0 }; }
static GlassMetadataResponse fixture_options(uint64_t request) { return (GlassMetadataResponse){ &optionMetadata[1], 0 }; }
void *fixture_dlopen(const char *path, int flags) { return (void *)1; }
char *fixture_dlerror(void) { return "fixture loader error"; }
void *fixture_dlsym(void *handle, const char *name) {
    if (!strcmp(name, "$s7SwiftUI6_GlassVMa")) return fixture_glass;
    if (!strcmp(name, "$s7SwiftUI6_GlassV15SiriWaveOptionsV11AudioMetersVMa")) return fixture_meters;
    if (!strcmp(name, "$s7SwiftUI6_GlassV15SiriWaveOptionsVMa")) return fixture_options;
    return (void *)1; // Required symbols are checked for presence, never called.
}
static int inspect(char *error) {
    uint64_t size = 0, stride = 0;
    return SiriGlassCheckPrivateCompatibility(40, 40, &size, &stride, error, 512);
}
int main(void) {
    glassMetadata[0] = glassWitness; meterMetadata[0] = meterWitness; optionMetadata[0] = optionWitness;
    glassWitness[1] = meterWitness[1] = optionWitness[1] = 1;
    glassWitness[8] = glassWitness[9] = 40;
    meterWitness[8] = meterWitness[9] = 16;
    optionWitness[8] = optionWitness[9] = 48;
    optionWitness[6] = 1;
    char error[512] = {0};
    if (inspect(error) != 1) { fprintf(stderr, "valid fixture rejected: %s\n", error); return 1; }
    optionWitness[6] = 0;
    if (inspect(error) != 0 || !strstr(error, "Optional tag witness")) {
        fprintf(stderr, "missing Optional tag witness was not rejected: %s\n", error); return 1;
    }
    optionWitness[6] = 1;
    if (inspect(error) != 1) { fprintf(stderr, "restored fixture rejected: %s\n", error); return 1; }
    meterWitness[9] = 24;
    if (inspect(error) != 0 || !strstr(error, "layout/destroy witness mismatch")) {
        fprintf(stderr, "bad layout was not rejected: %s\n", error); return 1;
    }
    puts("PASS native metadata admission, missing Optional witness and layout rejection");
    return 0;
}
