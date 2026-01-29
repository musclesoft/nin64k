; ============================================================================
; Stream data and copy routine - single stream version
; ============================================================================
;
; Exports:
;   copy_streams, copy_bytes_bwd
;   STREAM_DEST, STREAM_SIZE
;
; ============================================================================

.ifndef zp_copy_rem
zp_copy_rem     = $02
.endif
.ifndef zp_copy_src_lo
zp_copy_src_lo  = $03
zp_copy_src_hi  = $04
.endif
.ifndef zp_copy_dst_lo
zp_copy_dst_lo  = $05
zp_copy_dst_hi  = $06
.endif

; ----------------------------------------------------------------------
; Size and destination calculations (forward references to labels below)
; ----------------------------------------------------------------------
STREAM_SIZE = stream_end - stream_data

; Stream goes to high memory (ends at $FFFD, leaving $FFFE-$FFFF for IRQ vector)
STREAM_DEST = $10000 - STREAM_SIZE - 2

; ----------------------------------------------------------------------
; Copy compressed stream to destination for in-place decompression
; stream_data -> high memory (under ROMs)
; ----------------------------------------------------------------------
copy_streams:
        lda     #<stream_data
        sta     zp_copy_src_lo
        lda     #>stream_data
        sta     zp_copy_src_hi
        lda     #<STREAM_DEST
        sta     zp_copy_dst_lo
        lda     #>STREAM_DEST
        sta     zp_copy_dst_hi
        ldx     #>STREAM_SIZE
        lda     #<STREAM_SIZE
        sta     zp_copy_rem
        jmp     copy_bytes_bwd

; ----------------------------------------------------------------------
; Copy X full pages + remainder bytes, backwards
; Inputs: zp_copy_src, zp_copy_dst point to START
;         X = full pages, zp_copy_rem = remainder bytes
; ----------------------------------------------------------------------
copy_bytes_bwd:
        txa
        clc
        adc     zp_copy_src_hi
        sta     zp_copy_src_hi
        txa
        clc
        adc     zp_copy_dst_hi
        sta     zp_copy_dst_hi
        ldy     zp_copy_rem
        beq     @pages
        dey
@rem_loop:
        lda     (zp_copy_src_lo),y
        sta     (zp_copy_dst_lo),y
        dey
        cpy     #$FF
        bne     @rem_loop
@pages:
        txa
        beq     @done
@page_loop:
        dec     zp_copy_src_hi
        dec     zp_copy_dst_hi
        ldy     #$FF
@byte_loop:
        lda     (zp_copy_src_lo),y
        sta     (zp_copy_dst_lo),y
        dey
        cpy     #$FF
        bne     @byte_loop
        dex
        bne     @page_loop
@done:
        rts

; ----------------------------------------------------------------------
; Compressed stream data
; ----------------------------------------------------------------------
stream_data:
.ifdef USE_STREAM2
        .incbin "../generated/stream2.bin"
.else
        .incbin "../generated/stream.bin"
.endif
stream_end:
