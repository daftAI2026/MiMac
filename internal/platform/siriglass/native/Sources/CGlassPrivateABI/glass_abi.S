.globl _sw_call_x8
.globl _sw_call_x8_ret
.globl _sw_call_x0
.globl _sw_call_x0_x8
.globl _sw_call_x0_x1_x8
.globl _sw_call_x20_x0_x8
.globl _sw_call_x20_w0_x8
.globl _sw_options_init_power
.globl _sw_options_init_meters
.globl _g_subvariant_fn

.text
.align 2

// void sw_call_x8(void *fn, void *out)
_sw_call_x8:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x8, x1
    blr x0
    ldp x29, x30, [sp], #16
    ret

// uint64_t sw_call_x8_ret(void *fn, void *out)
_sw_call_x8_ret:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x8, x1
    blr x0
    ldp x29, x30, [sp], #16
    ret

// uint64_t sw_call_x0(void *fn, uint64_t arg)
_sw_call_x0:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x12, x0
    mov x0, x1
    blr x12
    ldp x29, x30, [sp], #16
    ret

// void sw_call_x0_x8(void *fn, void *arg0, void *out)
_sw_call_x0_x8:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x12, x0
    mov x0, x1
    mov x8, x2
    blr x12
    ldp x29, x30, [sp], #16
    ret

// void sw_call_x0_x1_x8(void *fn, void *arg0, void *arg1, void *out)
_sw_call_x0_x1_x8:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x12, x0
    mov x0, x1
    mov x1, x2
    mov x8, x3
    blr x12
    ldp x29, x30, [sp], #16
    ret

// void sw_call_x20_x0_x8(void *fn, void *self, void *arg0, void *out)
// Struct methods on _Glass keep self in x20. x8 is the indirect result.
_sw_call_x20_x0_x8:
    stp x20, x30, [sp, #-16]!
    mov x12, x0
    mov x20, x1
    mov x0, x2
    mov x8, x3
    blr x12
    ldp x20, x30, [sp], #16
    ret

// void sw_call_x20_w0_x8(void *fn, void *self, uint32_t arg0, void *out)
// Bool modifiers use w0; _Glass self remains in x20 and the result in x8.
_sw_call_x20_w0_x8:
    stp x20, x30, [sp, #-16]!
    mov x12, x0
    mov x20, x1
    mov w0, w2
    mov x8, x3
    blr x12
    ldp x20, x30, [sp], #16
    ret

// void sw_options_init_power(void *fn, void *out, void *personality, const void *args)
// args: double ax, double ay, float opacity, float db
_sw_options_init_power:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    ldr d0, [x3, #0]
    ldr d1, [x3, #8]
    ldr s2, [x3, #16]
    ldr s3, [x3, #20]
    mov x8, x1
    mov x12, x0
    mov x0, x2
    blr x12
    ldp x29, x30, [sp], #16
    ret

// void sw_options_init_meters(void *fn, void *out, void *personality,
//                             const void *meters, const void *anchorOpacity)
// anchorOpacity: double x, double y, float opacity.
// ABI: d0/d1 = anchor, s2 = opacity, x0 = one-byte personality address,
// x1 = 16-byte AudioMeters value, x8 = 48-byte indirect SiriWaveOptions result.
_sw_options_init_meters:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    mov x12, x0
    mov x8, x1
    ldr d0, [x4, #0]
    ldr d1, [x4, #8]
    ldr s2, [x4, #16]
    mov x0, x2
    mov x1, x3
    blr x12
    ldp x29, x30, [sp], #16
    ret

// Swift (String, UnsafeRawPointer, UnsafeMutableRawPointer) -> Void
// x0,x1 = String bits passed through as String?
// x2 = source _Glass, x3 = destination _Glass
.globl _gw_subvariant
_gw_subvariant:
    stp x20, x30, [sp, #-16]!
    mov x20, x2
    mov x8, x3
    adrp x16, _g_subvariant_fn@PAGE
    ldr x16, [x16, _g_subvariant_fn@PAGEOFF]
    blr x16
    ldp x20, x30, [sp], #16
    ret
