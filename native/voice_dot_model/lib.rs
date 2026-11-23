#[path = "src/rune_hud/mod.rs"]
mod rune_hud;

#[cfg(test)]
use std::mem::offset_of;
use std::mem::size_of;
use std::ptr;

use rune_hud::indicator::IndicatorModel;

pub const MI_DOT_MAX_DOTS: usize = 1024;
pub const MI_DOT_ABI_VERSION: u32 = 1;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct MiDotPose {
    pub width: f32,
    pub height: f32,
    pub opacity: f32,
    pub scale: f32,
    pub softness: f32,
    pub cool: f32,
    pub fail: f32,
    pub glow: f32,
    pub grid: f32,
    pub live: f32,
    pub reveal: f32,
    pub level: f32,
    pub clock: f32,
    pub done: f32,
    pub shift: f32,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct MiDotSnapshot {
    pub pose: MiDotPose,
    pub columns: u32,
    pub rows: u32,
    pub pitch: f32,
    pub dots: [[f32; 2]; MI_DOT_MAX_DOTS],
}

fn export_pose(pose: rune_hud::dot_scope::Pose) -> MiDotPose {
    MiDotPose {
        width: pose.width,
        height: pose.height,
        opacity: pose.opacity,
        scale: pose.scale,
        softness: pose.softness,
        cool: pose.cool,
        fail: pose.fail,
        glow: pose.glow,
        grid: pose.grid,
        live: pose.live,
        reveal: pose.reveal,
        level: pose.level,
        clock: pose.clock,
        done: pose.done,
        shift: pose.shift,
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn mi_dot_model_abi_version() -> u32 {
    MI_DOT_ABI_VERSION
}

#[unsafe(no_mangle)]
pub extern "C" fn mi_dot_model_create() -> *mut std::ffi::c_void {
    Box::into_raw(Box::new(IndicatorModel::new())).cast()
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_destroy(handle: *mut std::ffi::c_void) {
    if !handle.is_null() {
        unsafe { drop(Box::from_raw(handle.cast::<IndicatorModel>())) };
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_start(
    handle: *mut std::ffi::c_void,
    now_seconds: f64,
    reduce_motion: u8,
) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.start(now_seconds, reduce_motion != 0);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_meter_rms(handle: *mut std::ffi::c_void, rms: f32) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.meter_rms(rms);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_submit(
    handle: *mut std::ffi::c_void,
    job_id: u64,
    now_seconds: f64,
) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.submit(job_id, now_seconds);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_done(
    handle: *mut std::ffi::c_void,
    job_id: u64,
    now_seconds: f64,
) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.complete_job(job_id, now_seconds);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_fail(
    handle: *mut std::ffi::c_void,
    job_id: u64,
    now_seconds: f64,
) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.fail_job(job_id, now_seconds);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_cancel(
    handle: *mut std::ffi::c_void,
    job_id: u64,
    now_seconds: f64,
) {
    if let Some(model) = unsafe { handle.cast::<IndicatorModel>().as_mut() } {
        model.cancel(job_id, now_seconds);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mi_dot_model_step(
    handle: *mut std::ffi::c_void,
    now_seconds: f64,
    dt_seconds: f32,
    reduce_motion: u8,
    out: *mut MiDotSnapshot,
    out_len: usize,
) -> i32 {
    if handle.is_null() || out.is_null() {
        return -1;
    }
    if out_len < size_of::<MiDotSnapshot>() {
        return -2;
    }
    if !now_seconds.is_finite() || !dt_seconds.is_finite() {
        return -3;
    }
    unsafe {
        let Some(model) = handle.cast::<IndicatorModel>().as_mut() else {
            return -1;
        };
        let Some(active) = model.step(now_seconds, dt_seconds, reduce_motion != 0) else {
            return -3;
        };
        let columns = model.columns().min(96);
        let rows = model.rows().min(15);
        let count = columns
            .saturating_mul(rows)
            .min(MI_DOT_MAX_DOTS)
            .min(model.dots().len());
        let pose = export_pose(model.pose());

        let snapshot = out;
        ptr::addr_of_mut!((*snapshot).pose).write(pose);
        ptr::addr_of_mut!((*snapshot).columns).write(columns as u32);
        ptr::addr_of_mut!((*snapshot).rows).write(rows as u32);
        ptr::addr_of_mut!((*snapshot).pitch).write(model.pitch());
        ptr::copy_nonoverlapping(
            model.dots().as_ptr(),
            ptr::addr_of_mut!((*snapshot).dots).cast::<[f32; 2]>(),
            count,
        );
        if active { 1 } else { 0 }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn tick(model: &mut IndicatorModel, from: f64, seconds: f32) -> bool {
        model.step(from, seconds, false).unwrap()
    }

    #[test]
    fn abi_layout_matches_the_c_contract() {
        assert_eq!(size_of::<MiDotPose>(), 60);
        assert_eq!(offset_of!(MiDotSnapshot, pose), 0);
        assert_eq!(offset_of!(MiDotSnapshot, columns), 60);
        assert_eq!(offset_of!(MiDotSnapshot, rows), 64);
        assert_eq!(offset_of!(MiDotSnapshot, pitch), 68);
        assert_eq!(offset_of!(MiDotSnapshot, dots), 72);
        assert_eq!(size_of::<MiDotSnapshot>(), 8264);
    }

    #[test]
    fn stale_job_completion_does_not_replace_new_recording() {
        let mut model = IndicatorModel::new();
        model.start(1000.0, false);
        model.submit(41, 1000.2);
        model.start(1000.3, false);
        model.complete_job(41, 1000.3);
        tick(&mut model, 1000.3, 1.0 / 60.0);
        assert_eq!(model.status(), Some("Recording"));
    }

    #[test]
    fn initial_idle_model_hides_without_a_lifecycle_event() {
        let mut model = IndicatorModel::new();
        assert!(!tick(&mut model, 250.0, 1.0 / 60.0));
        assert_eq!(model.pose().opacity, 0.0);
    }

    #[test]
    fn capture_cancel_and_failure_before_submission_are_distinct() {
        let mut cancelled = IndicatorModel::new();
        cancelled.start(0.0, false);
        for n in 1..40 {
            tick(&mut cancelled, n as f64 / 60.0, 1.0 / 60.0);
        }
        cancelled.cancel(0, 0.7);
        for n in 42..120 {
            tick(&mut cancelled, n as f64 / 60.0, 1.0 / 60.0);
        }
        assert!(cancelled.pose().fail < 0.01);
        assert!(!cancelled.active());

        let mut failed = IndicatorModel::new();
        failed.start(0.0, false);
        for n in 1..25 {
            tick(&mut failed, n as f64 / 60.0, 1.0 / 60.0);
        }
        failed.fail_job(0, 0.4);
        for n in 25..60 {
            tick(&mut failed, n as f64 / 60.0, 1.0 / 60.0);
        }
        assert!(failed.pose().fail > 0.9);
        assert_eq!(failed.status(), Some("Failed"));
    }

    #[test]
    fn a_completed_job_has_minimum_processing_dwell_then_hides() {
        let mut model = IndicatorModel::new();
        model.start(0.0, false);
        model.submit(12, 0.05);
        model.complete_job(12, 0.10);
        tick(&mut model, 0.20, 1.0 / 60.0);
        assert_eq!(model.status(), Some("Transcribing"));
        for n in 13..40 {
            tick(&mut model, n as f64 / 60.0, 1.0 / 60.0);
        }
        assert_eq!(model.pose().done, 1.0);
        for n in 40..200 {
            tick(&mut model, n as f64 / 60.0, 1.0 / 60.0);
        }
        assert!(!model.active());
    }

    #[test]
    fn lifecycle_deadlines_match_exact_duration_boundaries() {
        let mut model = IndicatorModel::new();
        model.start(0.0, false);
        model.submit(7, 0.010);
        model.complete_job(7, 0.010);

        tick(&mut model, 0.209_999_999, 1.0 / 60.0);
        assert_eq!(model.status(), Some("Transcribing"));
        tick(&mut model, 0.210, 1.0 / 60.0);
        assert_eq!(model.pose().done, 1.0);

        tick(&mut model, 0.309_999_999, 1.0 / 60.0);
        assert!(model.active());
        tick(&mut model, 0.310, 1.0 / 60.0);
        assert_eq!(model.status(), None);
    }

    #[test]
    fn c_abi_writes_the_current_dot_field_without_exposing_extra_dots() {
        let handle = mi_dot_model_create();
        assert!(!handle.is_null());
        let mut out = MiDotSnapshot {
            pose: MiDotPose::default(),
            columns: 0,
            rows: 0,
            pitch: 0.0,
            dots: [[0.0; 2]; MI_DOT_MAX_DOTS],
        };
        unsafe { mi_dot_model_start(handle, 0.0, 0) };
        for frame in 1..=120 {
            unsafe { mi_dot_model_meter_rms(handle, 0.1) };
            let now = frame as f64 / 60.0;
            let status = unsafe {
                mi_dot_model_step(
                    handle,
                    now,
                    1.0 / 60.0,
                    0,
                    &mut out,
                    size_of::<MiDotSnapshot>(),
                )
            };
            assert_eq!(status, 1);
        }
        assert_eq!((out.columns, out.rows), (43, 9));
        assert!((out.pitch - 2.6).abs() < f32::EPSILON);
        assert!(
            out.dots[..out.columns as usize * out.rows as usize]
                .iter()
                .any(|dot| dot[0] > 0.0)
        );
        unsafe { mi_dot_model_destroy(handle) };
    }

    #[test]
    fn abi_rejects_short_snapshot_without_advancing_model() {
        let mut model = IndicatorModel::new();
        let mut storage = std::mem::MaybeUninit::<MiDotSnapshot>::uninit();
        let result = unsafe {
            mi_dot_model_step(
                (&mut model as *mut IndicatorModel).cast(),
                1.0,
                1.0 / 60.0,
                0,
                storage.as_mut_ptr(),
                size_of::<MiDotSnapshot>() - 1,
            )
        };
        assert_eq!(result, -2);
        assert!(!model.active());
    }
}
