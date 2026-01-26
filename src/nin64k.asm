; ============================================================================
; SounDemoN "Ninjas" - Clean Disassembly
; ============================================================================
;
; Original file: nin-soundemo
; Load address:  $0801
; Size:          2047 bytes
;
; Memory layout:
;   $0801-$080C  BASIC stub
;   $080D-$0A74  Main code
;   $0A75-$0BEC  Menu text and data
;   $0BED-$0BFF  Part timing data
;   $0C00-$0D1C  Disk loader code
;   $0D1D-$0E4F  1541 drive code
;   $0E50-$0E5F  Free space (for patches)
;   $0E60-$0F7F  Decompression routine
;   $0F80-$0FFF  Info screen and init
;
; Key variables:
;   $78 - Selected part from menu
;   $79 - Load next part flag (non-zero = load)
;   $7B - Current part number (1-9)
;
; Tune buffers:
;   $1000 - Buffer 1 (odd parts: 1,3,5,7,9)
;   $7000 - Buffer 2 (even parts: 2,4,6,8)
;
; ============================================================================

.setcpu "6502"

; Zero page
zp_selected     = $78
zp_load_flag    = $79
zp_col_count    = $7A
zp_part_num     = $7B
zp_msg_lo       = $8C
zp_msg_hi       = $8D
zp_scr_lo       = $8E
zp_scr_hi       = $8F

; Decompressor zero page (external interface)
zp_src_lo       = $02
zp_src_hi       = $03
zp_bitbuf       = $04
zp_out_lo       = $05
zp_out_hi       = $06

; Tune entry points
TUNE1_INIT      = $1000
TUNE1_PLAY      = $1003
TUNE2_INIT      = $7000
TUNE2_PLAY      = $7003

.segment "LOADADDR"
        .word   $0801

.segment "CODE"


; ----------------------------------------------------------------------
; BASIC stub: SYS 2066
; ----------------------------------------------------------------------
basic_stub:
        .word   $0810               ; Pointer to next BASIC line
        .word   8580                ; Line number
        .byte   $9E                 ; SYS token
        .byte   "2066 NIN!"         ; SYS address + decoration
        .byte   $00                 ; End of line
        .word   $0000               ; End of BASIC program

; ----------------------------------------------------------------------------
start:
        jsr     init_game
        sta     zp_part_num
        jsr     setup_irq
        lda     #<msg_loading
        sta     zp_msg_lo
        lda     #>msg_loading
        sta     zp_msg_hi
        jsr     print_msg
        jsr     load_d0
        jsr     load_and_init
        inc     zp_part_num
        lda     #<msg_title
        sta     zp_msg_lo
        lda     #>msg_title
        sta     zp_msg_hi
        jsr     print_msg
        lda     #$80
        sta     $028A

; ----------------------------------------------------------------------------
main_loop:
        lda     zp_load_flag
        bne     do_load_next
        ; Direct keyboard read (CIA1 at $DC00/$DC01, I/O already banked in)
        lda     #$7F                ; Select row 7
        sta     $DC00
        lda     $DC01
        and     #$10                ; Check bit 4 (space bar)
        bne     main_loop           ; Not pressed
@debounce:
        lda     $DC01               ; Wait for release
        and     #$10
        beq     @debounce
        ; Reset SID
        ldx     #$18
        lda     #$00
@sid:   sta     $D400,x
        dex
        bpl     @sid
        ; Advance to next song
        lda     zp_part_num
        cmp     #$09                ; Stop at song 9
        beq     main_loop
        lda     #$FF
        sta     zp_load_flag
        inc     zp_part_num
        jmp     main_loop

; ----------------------------------------------------------------------------
do_load_next:
        lda     #$CC
        sta     $0427
        jsr     load_and_init
        lda     #$00
        sta     zp_load_flag
        lda     #$20
        sta     $0427
        jmp     main_loop

; ----------------------------------------------------------------------------
setup_irq:
        sei
        lda     #$35                ; I/O on, KERNAL off
        sta     $01
        lda     #$7F
        sta     $DC0D               ; Disable CIA interrupts
        lda     #$01
        sta     $D01A               ; Enable VIC raster interrupt
        lda     $D011
        and     #$7F
        sta     $D011
        lda     #$33
        sta     $D012               ; Raster line $33
        lda     $DC0D               ; Acknowledge pending CIA
        ; Set up RAM-based IRQ vector at $FFFE/$FFFF
        lda     #$30                ; All RAM to write vectors
        sta     $01
        lda     #<irq_handler
        sta     $FFFE
        lda     #>irq_handler
        sta     $FFFF
        lda     #$35                ; Back to I/O mode
        sta     $01
        cli
        rts

; ----------------------------------------------------------------------------
irq_handler:
        pha
        lda     $01                 ; Save bank config
        pha
        txa
        pha
        tya
        pha
        lda     #$35                ; Bank in I/O
        sta     $01
        lda     $D019
        sta     $D019               ; Acknowledge VIC interrupt
        lda     #$16
        sta     $D018
        lda     zp_part_num
        cmp     zp_selected
        beq     @irq_done
        lda     #$07
        sta     $D020
        jsr     play_tick
        lda     #$00
        sta     $D020
@irq_done:
        pla
        tay
        pla
        tax
        pla
        sta     $01                 ; Restore bank config
        pla
        rti

; ----------------------------------------------------------------------------
play_tick:
        lda     zp_part_num
        and     #$01
        beq     L8CC
        jsr     $1003
        jmp     check_countdown

; ----------------------------------------------------------------------------
L8CC:
        jsr     $7003

; ----------------------------------------------------------------------------
check_countdown:
        lda     zp_part_num
        asl     a
        tax
        dex
        dex
        lda     #$FF
        clc
        adc     part_times,x
        sta     part_times,x
        lda     #$FF
        cmp     part_times,x
        bne     L8E8
        dec     part_times+1,x

; ----------------------------------------------------------------------------
L8E8:
        lda     part_times,x
        bne     play_done
        lda     part_times+1,x
        bne     play_done
        lda     zp_part_num
        cmp     #$09                ; Stop at song 9
        beq     play_done
        lda     #$FF
        sta     zp_load_flag
        inc     zp_part_num

; ----------------------------------------------------------------------------
play_done:
        rts

; ----------------------------------------------------------------------
; Copy streams wrapper with banking
; ----------------------------------------------------------------------
copy_streams_banked:
        sei                         ; Must disable IRQs when KERNAL banked out
        lda     #$30                ; All RAM (including $D000-$DFFF, no I/O)
        sta     $01
        jsr     copy_streams
        lda     #$37                ; Restore ROMs
        sta     $01
        cli                         ; Re-enable IRQs
        rts

; ----------------------------------------------------------------------------
clear_screen:
        lda     #$00
        sta     zp_msg_lo
        sta     zp_scr_lo
        lda     #$04
        sta     zp_msg_hi
        lda     #$D8
        sta     zp_scr_hi
        ldy     #$00

; ----------------------------------------------------------------------------
L90F:
        lda     #$20
        sta     (zp_msg_lo),y
        lda     #$0E
        sta     (zp_scr_lo),y
        lda     #$00
        sec
        adc     zp_msg_lo
        sta     zp_msg_lo
        lda     #$00
        adc     zp_msg_hi
        sta     zp_msg_hi
        lda     #$00
        sec
        adc     zp_scr_lo
        sta     zp_scr_lo
        lda     #$00
        adc     zp_scr_hi
        sta     zp_scr_hi
        lda     #$E8
        cmp     zp_msg_lo
        bne     L90F
        lda     #$07
        cmp     zp_msg_hi
        bne     L90F
        rts

; ----------------------------------------------------------------------------
print_msg:
        lda     zp_msg_lo
        pha
        lda     zp_msg_hi
        pha
        jsr     clear_screen
        pla
        sta     zp_msg_hi
        pla
        sta     zp_msg_lo

; ----------------------------------------------------------------------------
print_string:
        lda     #$00
        sta     zp_scr_lo
        lda     #$04
        sta     zp_scr_hi
        lda     #$28
        sta     zp_col_count
        ldy     #$00
print_loop:
        lda     (zp_msg_lo),y
        beq     print_done
        cmp     #$0D
        bne     L977
        lda     zp_col_count
        clc
        adc     zp_scr_lo
        sta     zp_scr_lo
        lda     #$00
        adc     zp_scr_hi
        sta     zp_scr_hi
        lda     #$28
        sta     zp_col_count
        jmp     advance_src

; ----------------------------------------------------------------------------
L977:
        cmp     #$41
        bcc     L982
        cmp     #$5B
        bcs     L982
        sec
        sbc     #$40

; ----------------------------------------------------------------------------
L982:
        cmp     #$C1
        bcc     L98D
        cmp     #$DB
        bcs     L98D
        sec
        sbc     #$80

; ----------------------------------------------------------------------------
L98D:
        sta     (zp_scr_lo),y
        dec     zp_col_count
        bne     L997
        lda     #$28
        sta     zp_col_count

; ----------------------------------------------------------------------------
L997:
        lda     #$00
        sec
        adc     zp_scr_lo
        sta     zp_scr_lo
        lda     #$00
        adc     zp_scr_hi
        sta     zp_scr_hi
advance_src:
        lda     #$00
        sec
        adc     zp_msg_lo
        sta     zp_msg_lo
        lda     #$00
        adc     zp_msg_hi
        sta     zp_msg_hi
        jmp     print_loop

; ----------------------------------------------------------------------------
print_done:
        rts

; ----------------------------------------------------------------------------
load_and_init:
        lda     zp_part_num
        cmp     #$09                ; Stop at song 9
        bne     L9BC
        rts

; ----------------------------------------------------------------------------
L9BC:
        ldx     #$44
        lda     zp_part_num
        clc
        adc     #$31
        tay
        jsr     load_tune
        lda     #$A1
        sta     $0427
        lda     zp_part_num
        and     #$01
        bne     init_buf2

; ----------------------------------------------------------------------------
; init_buf1: Called for EVEN $7B (0,2,4,6,8) → loads ODD files to $1000
init_buf1:
        lda     #$00
        jsr     TUNE1_INIT
load_done:
        rts

; ----------------------------------------------------------------------------
; init_buf2: Called for ODD $7B (1,3,5,7) → loads EVEN files to $7000
init_buf2:
        lda     #$00
        jsr     TUNE2_INIT
        rts

; ----------------------------------------------------------------------
; Message text
; ----------------------------------------------------------------------
msg_loading:
        .byte   $CC
        .byte   "OADING..."
        .byte   $00                     ; End of string
msg_title:
        .byte   $CE
        .byte   "INE "
        .byte   $C9
        .byte   "NCH "
        .byte   $CE
        .byte   "INJAS"
        .byte   $0D                     ; CR
        .byte   " BY "
        .byte   $D3
        .byte   $4F, $55, $4E, $C4, $45, $4D, $4F, $CE, $20, $20, $20, $20, $20, $20, $20, $20
        .byte   "     "
        .byte   $22, $0D, $20, $20, $C3, $4F, $50, $59, $52, $49, $47, $48, $54, $28, $43, $29
        .byte   " 2000 "
        .byte   $CF
        .byte   "TTO "
        .byte   $CA
        .byte   "ARVINEN"
        .byte   $0D                     ; CR
        .byte   $0D                     ; CR
        .byte   $C7
        .byte   "REETS TO:"
        .byte   $0D                     ; CR
        .byte   $0D                     ; CR
        .byte   "                 "
        .byte   $22, $20, $20, $20, $22, $0D, $C1, $47, $45, $4D, $49, $58, $45, $52, $20, $20
        .byte   $CD
        .byte   "ARKO "
        .byte   $CD
        .byte   "AKELA"
        .byte   $0D                     ; CR
        .byte   $C1
        .byte   $CD
        .byte   $CA
        .byte   "       "
        .byte   $CD
        .byte   $43, $46, $0D, $C7, $45, $45, $4C, $20, $20, $20, $20, $20, $20, $D2, $4F, $4E
        .byte   $45, $53, $0D, $C7, $52, $55, $45, $20, $20, $20, $20, $20, $20, $D4, $C2, $C2
        .byte   $0D                     ; CR
        .byte   $CA
        .byte   "EFF      "
        .byte   $DA
        .byte   $45, $44, $0D, $CA, $5A, $55, $20, $20, $20, $20, $20, $20, $20, $DA, $49, $4C
        .byte   $4F, $47, $0D, $CC, $4F, $4C, $CF, $CC, $CF, $4C, $4F, $00

; ----------------------------------------------------------------------
; Part timing data (9 parts x 2 bytes)
; ----------------------------------------------------------------------
part_times:
        .word   $BB44
        .word   $7234
        .word   $57C0
        .word   $0000
        .word   $B90A
        .word   $79F6
        .word   $491A
        .word   $7BF0
        .word   $0100
        brk

; ----------------------------------------------------------------------------
load_d0:
        jmp     load_d0_impl

; ----------------------------------------------------------------------------
load_tune:
        jmp     load_tune_impl

; ----------------------------------------------------------------------------
; Initialize stream pointer for in-memory decompression
; ----------------------------------------------------------------------------
load_d0_impl:
        lda     #<STREAM_MAIN_DEST
        sta     zp_src_lo
        lda     #>STREAM_MAIN_DEST
        sta     zp_src_hi
        lda     #$80
        sta     zp_bitbuf
        rts

; ----------------------------------------------------------------------------
; Load tune from memory using in-place decompression
; X, Y = ignored (were filename chars for disk load)
; Returns: C=0 success, C=1 error (not used for memory load)
; ----------------------------------------------------------------------------
load_tune_impl:
        ; Set output destination based on part number
        lda     #$00
        sta     zp_out_lo
        lda     zp_part_num
        and     #$01
        beq     @even_part
        lda     #$70                ; Odd part num -> $7000
        bne     @do_decompress
@even_part:
        lda     #$10                ; Even part num -> $1000
@do_decompress:
        sta     zp_out_hi
        ; Call decompressor in all-RAM mode (stream spans I/O region)
        lda     #$30                ; All RAM
        sta     $01
        jsr     decompress
        ; Song 9 is split: stream_main has first part, stream_tail has rest
        lda     zp_part_num
        cmp     #$08                ; Song 9 = part 8
        bne     @load_done
        lda     #<STREAM_TAIL_DEST
        sta     zp_src_lo
        lda     #>STREAM_TAIL_DEST
        sta     zp_src_hi
        lda     #$80
        sta     zp_bitbuf
        jsr     decompress
@load_done:
        lda     #$35                ; Back to I/O mode
        sta     $01
        clc                         ; Success
        rts

; ============================================================================
; V23 Decompressor - generated by ./compress
; Setup: zp_src, zp_bitbuf=$80, zp_out ($1000 or $7000)
; ============================================================================
checkpoint:
        rts
.include "../generated/decompress.asm"

; ----------------------------------------------------------------------------
; ----------------------------------------------------------------------------
init_game:
        jsr     copy_streams_banked ; Copy compressed data to safe location
        lda     #$00
        sta     zp_selected         ; Auto-select part 1 (skip menu)
        sta     $D020
        sta     $D021
        ldx     #$00

; ----------------------------------------------------------------------------
LFCD:
        lda     init_timing_data,x
        sta     part_times,x
        inx
        cpx     #$12
        bne     LFCD
        lda     #$FF
        sta     zp_load_flag
        lda     zp_selected
        rts

; ----------------------------------------------------------------------
; Initial part timing data
; ----------------------------------------------------------------------
init_timing_data:
.include "part_times.inc"

.include "stream.inc"
