/*
 Derived ABI thunk; see DERIVATION.md and the local Glass cross-check evidence.
 The Swift caller supplies the private input buffer address in x0 and its typed
 Glass result storage in x8. The resolved initializer writes the public result.
*/
.globl _siri_glass_init_public_from_private
.extern _g_glass_explicit_init_fn

.text
.align 2
_siri_glass_init_public_from_private:
    stp x29, x30, [sp, #-16]!
    mov x29, sp
    adrp x16, _g_glass_explicit_init_fn@PAGE
    ldr x16, [x16, _g_glass_explicit_init_fn@PAGEOFF]
    blr x16
    ldp x29, x30, [sp], #16
    ret
