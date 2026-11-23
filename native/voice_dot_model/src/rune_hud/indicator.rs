use std::collections::BTreeSet;
use std::time::{Duration, Instant};

use super::dot_scope::{self, Frame, Pose, Scope, Stage, Tuning};
use super::hud_placement::Island;

const SILENT: f32 = -120.0;
const PROCESSING_MIN_DURATION: Duration = Duration::from_millis(200);
const COMPLETED_VISIBLE_DURATION: Duration = Duration::from_millis(100);
const EARS_COMPLETED_VISIBLE_DURATION: Duration = Duration::from_millis(700);
const FAILED_VISIBLE_DURATION: Duration = Duration::from_millis(600);
#[allow(dead_code)]
const FIELD_EDGE_FADE: f32 = 3.0;

#[derive(Debug, Eq, PartialEq)]
enum HudState {
    Hidden,
    Recording {
        jobs: BTreeSet<u64>,
    },
    Transcribing {
        jobs: BTreeSet<u64>,
        working_since: Instant,
    },
    Completing {
        working_since: Instant,
    },
    Completed {
        started: Instant,
    },
    Failed {
        started: Instant,
    },
}

#[derive(Clone, Copy)]
enum JobOutcome {
    Completed,
    Cancelled,
    Failed,
}

impl HudState {
    fn take_jobs(&mut self) -> BTreeSet<u64> {
        match self {
            Self::Recording { jobs } | Self::Transcribing { jobs, .. } => std::mem::take(jobs),
            Self::Hidden
            | Self::Completing { .. }
            | Self::Completed { .. }
            | Self::Failed { .. } => BTreeSet::new(),
        }
    }

    #[cfg(test)]
    fn status(&self) -> Option<&'static str> {
        match self {
            Self::Recording { .. } => Some("Recording"),
            Self::Transcribing { .. } | Self::Completing { .. } => Some("Transcribing"),
            Self::Failed { .. } => Some("Failed"),
            Self::Hidden | Self::Completed { .. } => None,
        }
    }

    fn stage(&self) -> Option<Stage> {
        match self {
            Self::Recording { .. } => Some(Stage::Recording),
            Self::Transcribing { .. } | Self::Completing { .. } => Some(Stage::Processing),
            Self::Completed { .. } => Some(Stage::Done),
            Self::Failed { .. } => Some(Stage::Failed),
            Self::Hidden => None,
        }
    }
}

#[allow(dead_code)]
fn field_edge(tuning: &Tuning) -> f32 {
    let outer = (tuning.height - (tuning.rows - 1.0) * tuning.spacing) / 2.0;
    if outer < FIELD_EDGE_FADE && tuning.rows > 2.0 {
        outer + tuning.spacing
    } else {
        outer
    }
}

#[allow(dead_code)]
fn fill_island(tuning: Tuning, island: Option<Island>) -> Tuning {
    let Some(island) = island.filter(|island| !island.ears) else {
        return tuning;
    };
    let pitch = tuning.spacing;
    let edge = field_edge(&tuning);
    let columns = tuning.columns_in(island.plate_width(), edge);
    let width = (columns - 1.0) * pitch + 2.0 * edge;
    Tuning {
        columns,
        width,
        processing_width: width,
        ..tuning
    }
}

pub struct IndicatorModel {
    state: HudState,
    scope: Scope,
    stage: Stage,
    #[allow(dead_code)]
    tuning: Tuning,
    island: Option<Island>,
    loudness: f32,
    active: bool,
    clock_base: Instant,
    timestamp_origin: Option<f64>,
    last_now: Option<Instant>,
}

impl IndicatorModel {
    pub fn new() -> Self {
        let tuning = Tuning::default();
        Self {
            state: HudState::Hidden,
            scope: Scope::new(tuning),
            stage: Stage::Recording,
            tuning,
            island: None,
            loudness: SILENT,
            active: false,
            clock_base: Instant::now(),
            timestamp_origin: None,
            last_now: None,
        }
    }

    fn monotonic_now(&mut self, now: f64) -> Option<Instant> {
        if !now.is_finite() {
            return None;
        }
        let origin = *self.timestamp_origin.get_or_insert(now);
        let offset = Duration::try_from_secs_f64((now - origin).max(0.0)).ok()?;
        let now = self.clock_base.checked_add(offset)?;
        let now = self.last_now.map_or(now, |last| now.max(last));
        self.last_now = Some(now);
        Some(now)
    }

    pub fn start(&mut self, now: f64, reduce_motion: bool) {
        let Some(_now) = self.monotonic_now(now) else {
            return;
        };
        self.active = true;
        let jobs = self.state.take_jobs();
        self.state = HudState::Recording { jobs };
        self.loudness = SILENT;
        self.stage = Stage::Recording;
        self.scope.restart(reduce_motion);
    }

    #[allow(dead_code)]
    pub fn set_island(&mut self, island: Option<Island>) {
        if self.island != island {
            self.island = island;
            self.scope.tune(fill_island(self.tuning, island));
        }
    }

    pub fn meter_rms(&mut self, rms: f32) {
        self.active = true;
        self.loudness = if rms.is_finite() {
            dot_scope::loudness(rms)
        } else {
            SILENT
        };
    }

    pub fn submit(&mut self, job_id: u64, now: f64) {
        self.active = true;
        let Some(now) = self.monotonic_now(now) else {
            return;
        };
        if job_id == 0 {
            return;
        }
        let working_since = match self.state {
            HudState::Transcribing { working_since, .. }
            | HudState::Completing { working_since } => working_since,
            _ => now,
        };
        let mut jobs = self.state.take_jobs();
        jobs.insert(job_id);
        self.state = HudState::Transcribing {
            jobs,
            working_since,
        };
    }

    pub fn complete_job(&mut self, job_id: u64, now: f64) {
        self.active = true;
        let Some(now) = self.monotonic_now(now) else {
            return;
        };
        if job_id != 0 {
            self.finish_job(job_id, now, JobOutcome::Completed);
        }
    }

    pub fn cancel(&mut self, job_id: u64, now: f64) {
        self.active = true;
        let Some(now) = self.monotonic_now(now) else {
            return;
        };
        if job_id == 0 {
            self.finish_capture(now, false);
        } else {
            self.finish_job(job_id, now, JobOutcome::Cancelled);
        }
    }

    pub fn fail_job(&mut self, job_id: u64, now: f64) {
        self.active = true;
        let Some(now) = self.monotonic_now(now) else {
            return;
        };
        if job_id == 0 {
            self.finish_capture(now, true);
        } else {
            self.finish_job(job_id, now, JobOutcome::Failed);
        }
    }

    fn finish_capture(&mut self, now: Instant, failed: bool) {
        match std::mem::replace(&mut self.state, HudState::Hidden) {
            HudState::Recording { jobs } if !jobs.is_empty() => {
                self.state = HudState::Transcribing {
                    jobs,
                    working_since: now,
                };
            }
            HudState::Transcribing {
                jobs,
                working_since,
            } => {
                self.state = HudState::Transcribing {
                    jobs,
                    working_since,
                };
            }
            _ => {
                self.state = if failed {
                    HudState::Failed { started: now }
                } else {
                    HudState::Hidden
                };
            }
        }
    }

    fn finish_job(&mut self, job_id: u64, now: Instant, outcome: JobOutcome) {
        match &mut self.state {
            HudState::Recording { jobs } => {
                jobs.remove(&job_id);
            }
            HudState::Transcribing {
                jobs,
                working_since,
            } => {
                if !jobs.remove(&job_id) || !jobs.is_empty() {
                    return;
                }
                let working_since = *working_since;
                self.state = match outcome {
                    JobOutcome::Completed => HudState::Completing { working_since },
                    JobOutcome::Cancelled => HudState::Hidden,
                    JobOutcome::Failed => HudState::Failed { started: now },
                };
            }
            HudState::Hidden
            | HudState::Completing { .. }
            | HudState::Completed { .. }
            | HudState::Failed { .. } => {}
        }
    }

    pub fn step(&mut self, now: f64, dt: f32, reduce_motion: bool) -> Option<bool> {
        if !dt.is_finite() {
            return None;
        }
        let now = self.monotonic_now(now)?;
        let dt = dt.clamp(1.0 / 240.0, 1.0 / 20.0);
        let ears = self.island.is_some_and(|island| island.ears);
        let completed_duration = if ears {
            EARS_COMPLETED_VISIBLE_DURATION
        } else {
            COMPLETED_VISIBLE_DURATION
        };
        match self.state {
            HudState::Completing { working_since }
                if now.saturating_duration_since(working_since) >= PROCESSING_MIN_DURATION =>
            {
                self.state = HudState::Completed { started: now };
            }
            HudState::Completed { started }
                if now.saturating_duration_since(started) >= completed_duration =>
            {
                self.state = HudState::Hidden;
            }
            HudState::Failed { started }
                if now.saturating_duration_since(started) >= FAILED_VISIBLE_DURATION =>
            {
                self.state = HudState::Hidden;
            }
            _ => {}
        }
        let visible = !matches!(self.state, HudState::Hidden);
        if let Some(stage) = self.state.stage() {
            self.stage = stage;
        }
        self.scope.advance(
            dt,
            Frame {
                stage: self.stage,
                visible,
                loudness: self.loudness,
                reduce_motion,
            },
        );
        self.active = visible || !self.scope.settled();
        Some(self.active)
    }

    pub fn pose(&self) -> Pose {
        self.scope.pose()
    }

    pub fn columns(&self) -> usize {
        self.scope.columns()
    }

    pub fn rows(&self) -> usize {
        self.scope.rows()
    }

    pub fn pitch(&self) -> f32 {
        self.scope.tuning().spacing
    }

    pub fn dots(&self) -> &[[f32; 2]] {
        self.scope.dots()
    }

    #[cfg(test)]
    pub fn active(&self) -> bool {
        self.active
    }

    #[cfg(test)]
    pub fn status(&self) -> Option<&'static str> {
        self.state.status()
    }
}
