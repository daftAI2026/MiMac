#pragma once

#include <stdint.h>

const char *glass_last_error(void);
void *glass_compose(int32_t kind, int32_t personality, float db);
void *glass_alloc(void);
void *glass_siri_base(void);
void *glass_apply_audio_meters_wave(const void *glass,
                                    const void *amplitudesStorage,
                                    const void *frequenciesStorage,
                                    int32_t personality);
void *glass_apply_lense(const void *glass);
void *glass_excluding_shadow(const void *glass, int32_t excludesShadow);
void glass_destroy(void *glass);
int32_t glass_personality_byte(int32_t index);
extern void *g_glass_explicit_init_fn;
