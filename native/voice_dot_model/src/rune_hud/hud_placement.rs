#[allow(dead_code)]
pub const ISLAND_INSET: f32 = 7.0;

#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Island {
    pub notch: [f32; 2],
    pub bar: f32,
    pub width: f32,
    pub top: f32,
    pub ears: bool,
}

impl Island {
    #[allow(dead_code)]
    pub fn plate_width(&self) -> f32 {
        self.width - 2.0 * ISLAND_INSET
    }
}
