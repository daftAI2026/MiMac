use crate::rune_hud::spring::MORPH;

mod scope;
pub use scope::*;

pub const MAX_COLUMNS: usize = 96;
pub const MAX_ROWS: usize = 15;
pub const MAX_DOTS: usize = 1024;
pub const PLATE_HEIGHT: f32 = 26.0;

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Tuning {
    pub columns: f32,
    pub rows: f32,
    pub spacing: f32,
    pub width: f32,
    pub processing_width: f32,
    pub height: f32,
    pub braid_speed: f32,
    pub helix_cycles: f32,
    pub gain: f32,
    pub response: f32,
    pub release: f32,
    pub history_speed: f32,
    pub phosphor: f32,
    pub glow: f32,
    pub saturation: f32,
    pub shimmer_pass: f32,
    pub shimmer: f32,
    pub transition: f32,
    pub bounce: f32,
    pub auto_level: bool,
    pub auto_release: f32,
    pub noise_floor: f32,
    pub processing: Processing,
    pub placement: Placement,
    pub pretend_notch: bool,
    pub island_spread: f32,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[allow(dead_code)]
pub enum Processing {
    Sweep,
    #[default]
    Flow,
    Pulse,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[allow(dead_code)]
pub enum Placement {
    #[default]
    Below,
    MenuBar,
    Notch,
}

impl Default for Tuning {
    fn default() -> Self {
        Self {
            columns: 43.0,
            rows: 9.0,
            spacing: 2.6,
            width: 112.0,
            processing_width: 76.0,
            height: PLATE_HEIGHT,
            braid_speed: 1.0,
            helix_cycles: 1.6,
            gain: 1.0,
            response: 6.5,
            release: 0.15,
            history_speed: 150.0,
            phosphor: 0.6,
            glow: 1.0,
            saturation: 1.0,
            shimmer_pass: 0.55,
            shimmer: 1.0,
            transition: MORPH.duration,
            bounce: MORPH.bounce,
            auto_level: true,
            auto_release: 3.0,
            noise_floor: -60.0,
            processing: Processing::default(),
            placement: Placement::default(),
            pretend_notch: false,
            island_spread: 28.0,
        }
    }
}

#[derive(Clone, Copy)]
pub struct Knob {
    min: f32,
    max: f32,
    whole: bool,
    index: usize,
}

const fn knob(index: usize, min: f32, max: f32) -> Knob {
    Knob {
        min,
        max,
        whole: false,
        index,
    }
}

const fn whole(index: usize, min: f32, max: f32) -> Knob {
    Knob {
        whole: true,
        ..knob(index, min, max)
    }
}

pub const KNOBS: [Knob; 22] = [
    whole(0, 11.0, MAX_COLUMNS as f32),
    whole(1, 3.0, MAX_ROWS as f32),
    knob(2, 1.6, 4.0),
    knob(3, 40.0, 280.0),
    knob(4, 26.0, 280.0),
    knob(5, 14.0, 40.0),
    knob(6, 0.0, 3.0),
    knob(7, 0.0, 4.0),
    knob(8, 0.25, 3.0),
    knob(9, 1.0, 16.0),
    knob(10, 0.03, 0.6),
    knob(11, 40.0, 400.0),
    knob(12, 0.05, 1.5),
    knob(13, 0.0, 3.0),
    knob(14, 0.0, 2.5),
    knob(15, 0.2, 1.5),
    knob(16, 0.0, 2.5),
    knob(17, 0.1, 0.8),
    knob(18, 0.0, 0.6),
    knob(19, 0.5, 10.0),
    knob(20, -80.0, -30.0),
    knob(21, 0.0, 60.0),
];

impl Knob {
    fn get(&self, tuning: &Tuning) -> f32 {
        let mut copy = *tuning;
        *copy.slot(self.index)
    }

    fn set(&self, tuning: &mut Tuning, value: f32) {
        let value = value.clamp(self.min, self.max);
        *tuning.slot(self.index) = if self.whole { value.round() } else { value };
    }
}

impl Tuning {
    fn slot(&mut self, index: usize) -> &mut f32 {
        match index {
            0 => &mut self.columns,
            1 => &mut self.rows,
            2 => &mut self.spacing,
            3 => &mut self.width,
            4 => &mut self.processing_width,
            5 => &mut self.height,
            6 => &mut self.braid_speed,
            7 => &mut self.helix_cycles,
            8 => &mut self.gain,
            9 => &mut self.response,
            10 => &mut self.release,
            11 => &mut self.history_speed,
            12 => &mut self.phosphor,
            13 => &mut self.glow,
            14 => &mut self.saturation,
            15 => &mut self.shimmer_pass,
            16 => &mut self.shimmer,
            17 => &mut self.transition,
            18 => &mut self.bounce,
            19 => &mut self.auto_release,
            20 => &mut self.noise_floor,
            _ => &mut self.island_spread,
        }
    }

    #[allow(dead_code)]
    pub fn columns_in(&self, room: f32, edge: f32) -> f32 {
        (((room - 2.0 * edge) / self.spacing + 1e-3).floor() + 1.0).max(1.0)
    }

    pub fn clamped(mut self) -> Self {
        for knob in &KNOBS {
            let value = knob.get(&self);
            let value = if value.is_finite() {
                value
            } else {
                knob.get(&Self::default())
            };
            knob.set(&mut self, value);
        }
        let rows = (MAX_DOTS / self.columns as usize).min(MAX_ROWS);
        self.rows = self.rows.min(rows as f32);
        self
    }
}

pub fn loudness(rms: f32) -> f32 {
    20.0 * (rms.max(0.0) + 1e-6).log10()
}

pub fn fixed_level(loudness: f32) -> f32 {
    ((loudness + 40.0) / 30.0).clamp(0.0, 1.0)
}

const AUTO_MIN_SPAN: f32 = 16.0;
const AUTO_RANGE: f32 = 24.0;
const AUTO_ROOM_MARGIN: f32 = 5.0;
const AUTO_ATTACK: f32 = 0.04;
const ROOM_FALL: f32 = 0.3;
const ROOM_RISE: f32 = 6.0;

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct AutoLevel {
    peak: f32,
    room: f32,
}

impl Default for AutoLevel {
    fn default() -> Self {
        Self {
            peak: -30.0,
            room: -55.0,
        }
    }
}

impl AutoLevel {
    pub fn step(&mut self, loudness: f32, h: f32, release: f32, noise_floor: f32) -> f32 {
        let db = loudness.max(-120.0);
        let follow = |value: &mut f32, target: f32, time: f32| {
            *value += (target - *value) * (1.0 - (-h / time.max(1e-3)).exp());
        };
        let room_time = if db < self.room { ROOM_FALL } else { ROOM_RISE };
        follow(&mut self.room, db, room_time);
        let lowest = self.room.max(noise_floor) + AUTO_MIN_SPAN;
        if db > self.peak {
            follow(&mut self.peak, db, AUTO_ATTACK);
        } else {
            follow(&mut self.peak, lowest, release);
        }
        self.peak = self.peak.max(lowest);
        self.level(db, noise_floor)
    }

    pub fn level(&self, loudness: f32, noise_floor: f32) -> f32 {
        let low = (self.room.max(noise_floor) + AUTO_ROOM_MARGIN).max(self.peak - AUTO_RANGE);
        ((loudness - low) / (self.peak - low).max(1.0)).clamp(0.0, 1.0)
    }
}
