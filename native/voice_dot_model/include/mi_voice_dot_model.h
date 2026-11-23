#ifndef MI_VOICE_DOT_MODEL_H
#define MI_VOICE_DOT_MODEL_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define MI_DOT_MAX_DOTS 1024u
#define MI_DOT_ABI_VERSION 1u

typedef struct MiDotModel MiDotModel;

typedef struct MiDotPose {
    float width;
    float height;
    float opacity;
    float scale;
    float softness;
    float cool;
    float fail;
    float glow;
    float grid;
    float live;
    float reveal;
    float level;
    float clock;
    float done;
    float shift;
} MiDotPose;

typedef struct MiDotSnapshot {
    MiDotPose pose;
    uint32_t columns;
    uint32_t rows;
    float pitch;
    float dots[MI_DOT_MAX_DOTS][2];
} MiDotSnapshot;

uint32_t mi_dot_model_abi_version(void);
MiDotModel *mi_dot_model_create(void);
void mi_dot_model_destroy(MiDotModel *model);
void mi_dot_model_start(MiDotModel *model, double now_seconds, uint8_t reduce_motion);
void mi_dot_model_meter_rms(MiDotModel *model, float centered_linear_rms);
void mi_dot_model_submit(MiDotModel *model, uint64_t job_id, double now_seconds);
void mi_dot_model_done(MiDotModel *model, uint64_t job_id, double now_seconds);
/* job_id == 0 means the capture failed before a job was submitted. */
void mi_dot_model_fail(MiDotModel *model, uint64_t job_id, double now_seconds);
/* job_id == 0 cancels the active capture; otherwise it cancels that job. */
void mi_dot_model_cancel(MiDotModel *model, uint64_t job_id, double now_seconds);
/* 1 active, 0 hidden and settled, -1 invalid pointer, -2 short output, -3 invalid time/dt. */
int32_t mi_dot_model_step(
    MiDotModel *model,
    double now_seconds,
    float dt_seconds,
    uint8_t reduce_motion,
    MiDotSnapshot *out,
    size_t out_len);

#ifdef __cplusplus
}
#endif

#endif
