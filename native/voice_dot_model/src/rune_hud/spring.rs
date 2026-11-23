const CRITICAL_DAMPING_TOLERANCE: f32 = 1e-4;

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Spec {
    pub duration: f32,
    pub bounce: f32,
}

impl Spec {
    pub const fn new(duration: f32, bounce: f32) -> Self {
        Self { duration, bounce }
    }

    pub fn omega(self) -> f32 {
        std::f32::consts::TAU / self.duration.max(0.01)
    }

    pub fn zeta(self) -> f32 {
        if self.bounce >= 0.0 {
            (1.0 - self.bounce).max(0.0)
        } else {
            1.0 / (1.0 + self.bounce).max(0.01)
        }
    }
}

pub const MORPH: Spec = Spec::new(0.30, 0.15);
pub const REDUCED_FADE: Spec = Spec::new(0.16, 0.0);

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Spring {
    pub x: f32,
    pub v: f32,
    pub target: f32,
}

impl Spring {
    pub const fn at(x: f32) -> Self {
        Self {
            x,
            v: 0.0,
            target: x,
        }
    }

    pub fn jump(&mut self, x: f32) {
        self.x = x;
        self.v = 0.0;
        self.target = x;
    }

    pub fn step_to(&mut self, target: f32, spec: Spec, dt: f32) {
        self.target = target;
        self.step(spec, dt);
    }

    fn step(&mut self, spec: Spec, dt: f32) {
        if dt <= 0.0 || !dt.is_finite() {
            return;
        }
        let omega = spec.omega();
        let zeta = spec.zeta();
        let x0 = self.x - self.target;
        let v0 = self.v;

        if zeta < 1.0 - CRITICAL_DAMPING_TOLERANCE {
            let decay = zeta * omega;
            let omega_d = omega * (1.0 - zeta * zeta).sqrt();
            let exp = (-decay * dt).exp();
            let (sin, cos) = (omega_d * dt).sin_cos();
            let sin_over_d = sin / omega_d;
            self.x = self.target + exp * (x0 * (cos + decay * sin_over_d) + v0 * sin_over_d);
            self.v = exp * (-x0 * omega * omega * sin_over_d + v0 * (cos - decay * sin_over_d));
        } else if zeta > 1.0 + CRITICAL_DAMPING_TOLERANCE {
            let root = (zeta * zeta - 1.0).sqrt();
            let root_sum = zeta + root;
            let slow_root = -omega / root_sum;
            let fast_root = -omega * root_sum;
            let denom = slow_root - fast_root;
            let slow_exp = (slow_root * dt).exp();
            let fast_exp = (fast_root * dt).exp();
            let p00 = (-fast_root * slow_exp + slow_root * fast_exp) / denom;
            let p01 = (slow_exp - fast_exp) / denom;
            let p10 = slow_root * fast_root * (fast_exp - slow_exp) / denom;
            let p11 = (slow_root * slow_exp - fast_root * fast_exp) / denom;
            self.x = self.target + p00 * x0 + p01 * v0;
            self.v = p10 * x0 + p11 * v0;
        } else {
            let exp = (-omega * dt).exp();
            let c = v0 + omega * x0;
            self.x = self.target + (x0 + c * dt) * exp;
            self.v = (v0 - omega * c * dt) * exp;
        }
    }
}
