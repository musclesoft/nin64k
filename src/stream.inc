; ============================================================================
; Stream data and copy routines - shared across all builds
; ============================================================================
;
; Exports:
;   copy_streams, copy_bytes_bwd
;   STREAM_MAIN_DEST, STREAM_TAIL_DEST, STREAM_MAIN_SIZE, STREAM_TAIL_SIZE
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
STREAM_TAIL_SIZE = stream_main - stream_tail
STREAM_MAIN_SIZE = stream_end - stream_main

; stream_main goes to high memory (ends at $FFFD, leaving $FFFE-$FFFF for IRQ vector)
STREAM_MAIN_DEST = $10000 - STREAM_MAIN_SIZE - 2
; stream_tail goes to buffer A tail (largest odd song S5 ends at $663A)
STREAM_TAIL_DEST = $663B

; ----------------------------------------------------------------------
; Copy compressed streams to destinations for in-place decompression
; stream_main -> high memory (under ROMs)
; stream_tail -> $663B-$6FFF (buffer A tail)
; ----------------------------------------------------------------------
copy_streams:
        lda     #<stream_main
        sta     zp_copy_src_lo
        lda     #>stream_main
        sta     zp_copy_src_hi
        lda     #<STREAM_MAIN_DEST
        sta     zp_copy_dst_lo
        lda     #>STREAM_MAIN_DEST
        sta     zp_copy_dst_hi
        ldx     #>STREAM_MAIN_SIZE
        lda     #<STREAM_MAIN_SIZE
        sta     zp_copy_rem
        jsr     copy_bytes_bwd

        lda     #<stream_tail
        sta     zp_copy_src_lo
        lda     #>stream_tail
        sta     zp_copy_src_hi
        lda     #<STREAM_TAIL_DEST
        sta     zp_copy_dst_lo
        lda     #>STREAM_TAIL_DEST
        sta     zp_copy_dst_hi
        ldx     #>STREAM_TAIL_SIZE
        lda     #<STREAM_TAIL_SIZE
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
; Compressed stream data (tail before main for consistent copy direction)
; ----------------------------------------------------------------------
stream_tail:
        .incbin "../generated/stream_tail.bin"
stream_main:
.ifndef STREAM_OFFSET
    STREAM_OFFSET = 0
.endif
        .incbin "../generated/stream_main.bin", STREAM_OFFSET
stream_end:
