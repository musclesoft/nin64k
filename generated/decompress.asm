; Size: 259 bytes
; External zero page variables (must be defined by caller)
; zp_src_lo       = $02   ; Source pointer (compressed data)
; zp_src_hi       = $03
; zp_bitbuf       = $04   ; Bit buffer (set to $80 for first call)
; zp_out_lo       = $05   ; Output pointer
; zp_out_hi       = $06
;
; IMPORTANT: Define 'checkpoint' globally before including this file.
; Called frequently (every bit, every byte). Can trash A and P.
; Minimal: checkpoint: rts

; Buffer layout constants (dual-buffer decompression)
; Buffer 1 (odd songs):  $2000
; Buffer 2 (even songs): $7000
; To change: update constants in cmd/compress/decompress6502.go, then rebuild
DECOMP_BUF1_HI   = $20           ; Buffer 1 high byte
DECOMP_BUF2_HI   = $70           ; Buffer 2 high byte
DECOMP_BUF_GAP   = $50           ; Gap between buffers ($5000 >> 8)
DECOMP_WRAP_HI   = $C0           ; Buffer 2 + gap (wrap threshold)

; Internal zero page variables
zp_val_lo       = $07
zp_val_hi       = $08
zp_ref_lo       = $09
zp_ref_hi       = $0A
zp_other_delta  = $0B
zp_caller_x     = $0C

.proc decompress
        ldy     #$00
        lda     zp_out_hi
        cmp     #$70
        lda     #$B0
        bcc     store_delta
        lda     #$50
store_delta:
        sta     zp_other_delta
main_loop:
        ldx     #$01
        jsr     read_bit
        bcc     set_x3
        jsr     read_bit
        bcs     not_literal
        txa
literal_loop:
        jsr     read_bit
        rol a
        bcc     literal_loop
        sta     (zp_out_lo),y
        inc     zp_out_lo
        bne     main_loop
        inc     zp_out_hi
        bne     main_loop
not_literal:
        jsr     read_bit
        bcc     backref_common
        jsr     read_bit
        bcc     fwdref
        jsr     read_bit
        bcc     set_x2
fwdref:
        php
        jsr     read_expgol
        adc     zp_out_lo
        sta     zp_ref_lo
        txa
        adc     zp_out_hi
        plp
        bcc     store_and_check
        sbc     zp_other_delta
store_and_check:
        cmp     #$C0
        bcc     no_high_wrap
        sbc     #$A0
no_high_wrap:
        bne     backref_no_adjust
set_x3:
        inx
set_x2:
        inx
backref_common:
        jsr     read_expgol
        asl a
        php
        clc
        adc     zp_caller_x
        php
        clc
        adc     zp_val_lo
        sta     zp_val_lo
        txa
        rol a
        plp
        adc     zp_val_hi
        plp
        adc     #$00
        sta     zp_val_hi
        lda     zp_out_lo
        sec
        sbc     zp_val_lo
        sta     zp_ref_lo
        lda     zp_out_hi
        sbc     zp_val_hi
        bcc     backref_adjust
        cmp     #$20
        bcs     backref_no_adjust
backref_adjust:
        adc     #$A0
backref_no_adjust:
        sta     zp_ref_hi
        jsr     read_expgol
        adc     #$02
        tax
        bcc     copy_loop
        inc     zp_val_hi
copy_loop:
        lda     (zp_ref_lo),y
        sta     (zp_out_lo),y
        jsr     checkpoint
        inc     zp_out_lo
        bne     skip_out_hi_inc
        inc     zp_out_hi
skip_out_hi_inc:
        inc     zp_ref_lo
        bne     skip_ref_hi_inc
        inc     zp_ref_hi
skip_ref_hi_inc:
        txa
        bne     skip_val_hi_dec
        dec     zp_val_hi
skip_val_hi_dec:
        dex
        txa
        ora     zp_val_hi
        bne     copy_loop
        jmp     main_loop
read_expgol:
        stx     zp_caller_x
        ldx     #$01
        stx     zp_val_lo
        sty     zp_val_hi
count_zeros:
        dex
        cpx     #$F4
        beq     terminator
        jsr     read_bit
        bcc     count_zeros
        txa
        beq     gamma_done
read_gamma_bits:
        jsr     read_bit
        rol     zp_val_lo
        rol     zp_val_hi
        inx
        bne     read_gamma_bits
gamma_done:
        lda     zp_val_lo
        bne     dec_gamma
        dec     zp_val_hi
dec_gamma:
        dec     zp_val_lo
        asl     zp_val_lo
        rol     zp_val_hi
        asl     zp_val_lo
        rol     zp_val_hi
        tya
        jsr     read_bit
        rol a
        jsr     read_bit
        rol a
        ora     zp_val_lo
        sta     zp_val_lo
        ldx     zp_val_hi
        rts
read_bit:
        pha
        jsr     checkpoint
        asl     zp_bitbuf
        bne     read_bit_done
        lda     (zp_src_lo),y
        rol a
        sta     zp_bitbuf
        inc     zp_src_lo
        bne     read_bit_done
        inc     zp_src_hi
read_bit_done:
        pla
        rts
terminator:
        pla
        pla
        rts
; checkpoint: max 53 cycles between calls
.endproc
