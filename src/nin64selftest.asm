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
;   zp_part_num - Current part number (1-9)
;
; Tune buffers:
;   $2000 - Buffer 1 (odd parts: 1,3,5,7,9)
;   $4000 - Buffer 2 (even parts: 2,4,6,8)
;
; ============================================================================

.setcpu "6502"

; Zero page (avoid player's $10-$21)
zp_selected     = $7E
zp_load_flag    = $7F
zp_part_num     = $80
; Selftest zero page
zp_csum_lo      = $D9
zp_csum_hi      = $DA
zp_size_lo      = $DB
zp_size_hi      = $DC
zp_song_idx     = $DD
zp_screen_lo    = $DE
zp_screen_hi    = $DF
zp_copy_rem     = $E0
zp_ptr_lo       = $E1
zp_ptr_hi       = $E2
zp_copy_src_lo  = $E3
zp_copy_src_hi  = $E4
zp_copy_dst_lo  = $E5
zp_copy_dst_hi  = $E6

; Decompressor zero page (external interface)
zp_src_lo       = $02
zp_src_hi       = $03
zp_bitbuf       = $04
zp_out_lo       = $05
zp_out_hi       = $06

; Hardware
VIC_D011        = $D011
VIC_D012        = $D012
VIC_D018        = $D018
VIC_D019        = $D019
VIC_D01A        = $D01A
VIC_D020        = $D020
VIC_D021        = $D021
CIA1_DC0D       = $DC0D
CIA2_DD00       = $DD00

; KERNAL
SCNKEY          = $FF9F
GETIN           = $FFE4
CHROUT          = $FFD2
IRQ_RETURN      = $EA31

; Buffer destinations (must match DECOMP_BUF1_HI/DECOMP_BUF2_HI in decompress.asm)
; Note: selftest code extends to ~$1A00, so buffers must start after that
TUNE1_BASE      = $2000         ; Odd songs (1,3,5,7,9)
TUNE2_BASE      = $4000         ; Even songs (2,4,6,8)

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
        lda     #0
        sta     zp_selected
        sta     zp_part_num
        sei
        lda     #<ram_irq
        sta     $FFFE
        lda     #>ram_irq
        sta     $FFFF
        lda     #<kernal_irq
        sta     $0314
        lda     #>kernal_irq
        sta     $0315
        jsr     setup_irq
        cli

        lda     #$35
        sta     $01

        jsr     init
        jsr     init_stream

        jmp     selftest            ; Run selftest instead of normal startup
start_normal:
        jsr     load_and_init

; ----------------------------------------------------------------------------
main_loop:
        lda     zp_load_flag
        bne     do_load_next
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
        lda     #$7F
        sta     $DC0D
        lda     #$01
        sta     $D01A
        lda     $D011
        and     #$7F
        sta     $D011
        lda     #$33
        sta     $D012
        lda     $DC0D
        rts

kernal_irq:
        inc     $0425
        jsr irq_handler
        jmp     $ea7b

; ----------------------------------------------------------------------------
irq_handler:
        pha
        txa
        pha
        tya
        pha

        lda     $DC0D               ; Acknowledge CIA1 interrupt
        lda     $D019
        sta     $D019               ; Acknowledge VIC interrupt

        lda     #$16
        sta     $D018
        lda     #$07
        sta     $D020
        lda     zp_part_num
        beq     skip_play
        jsr     play_tick

; ----------------------------------------------------------------------------
skip_play:
        lda     #$00
        sta     $D020
        pla
        tay
        pla
        tax
        pla
        rts

; ----------------------------------------------------------------------------
play_tick:
        jsr     player_play
        rts

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
        cmp     #$07                ; Stop at song 7 (limited disk space)
        beq     play_done
        lda     #$FF
        sta     zp_load_flag
        inc     zp_part_num

; ----------------------------------------------------------------------------
play_done:
        rts

ram_irq:
        inc     $0427
        jsr     irq_handler
        rti

; ----------------------------------------------------------------------
; Print 16-bit hex word from zp_csum at screen position Y
; (Placed here to stay below $1000 - called during selftest)
; ----------------------------------------------------------------------
print_hex_word:
        lda     zp_csum_hi
        jsr     @print_hex_byte
        lda     zp_csum_lo
@print_hex_byte:
        pha
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        jsr     @print_nibble
        pla
        and     #$0F
@print_nibble:
        cmp     #$0A
        bcc     @digit
        adc     #$06
@digit:
        adc     #$30
        sta     (zp_screen_lo),y
        iny
        rts

; ----------------------------------------------------------------------
; Calculate checksum of decompressed output
; ----------------------------------------------------------------------
selftest_output_checksum:
        ldx     zp_song_idx
        txa
        asl     a
        tax
        lda     selftest_sizes,x
        sta     zp_size_lo
        lda     selftest_sizes+1,x
        sta     zp_size_hi
        lda     zp_song_idx
        and     #$01
        bne     @out_odd
        lda     #>TUNE1_BASE        ; Even index (0,2,4,6,8) -> TUNE1
        bne     @out_set
@out_odd:
        lda     #>TUNE2_BASE        ; Odd index (1,3,5,7) -> TUNE2
@out_set:
        sta     zp_ptr_hi
        lda     #$00
        sta     zp_ptr_lo
        jsr     calc_checksum
        rts

; ----------------------------------------------------------------------
; Calculate 16-bit additive checksum
; Input: zp_ptr = start address, zp_size = byte count
; Output: zp_csum = checksum
; ----------------------------------------------------------------------
calc_checksum:
        lda     #$00
        sta     zp_csum_lo
        sta     zp_csum_hi
        ldy     #$00
@csum_loop:
        lda     zp_size_lo
        ora     zp_size_hi
        beq     @csum_done
        lda     (zp_ptr_lo),y
        clc
        adc     zp_csum_lo
        sta     zp_csum_lo
        bcc     @no_carry
        inc     zp_csum_hi
@no_carry:
        inc     zp_ptr_lo
        bne     @no_ptr_carry
        inc     zp_ptr_hi
@no_ptr_carry:
        lda     zp_size_lo
        bne     @dec_lo
        dec     zp_size_hi
@dec_lo:
        dec     zp_size_lo
        jmp     @csum_loop
@csum_done:
        rts

; ----------------------------------------------------------------------
; Verify stream checksum (single stream)
; ----------------------------------------------------------------------
selftest_verify_stream:
        ldy     #0
        lda     #char_s
        sta     (zp_screen_lo),y
        iny
        lda     #char_t
        sta     (zp_screen_lo),y
        iny
        lda     #char_r
        sta     (zp_screen_lo),y
        iny
        lda     #char_colon
        sta     (zp_screen_lo),y
        iny
        sty     zp_copy_rem
        lda     #<STREAM_START
        sta     zp_ptr_lo
        lda     #>STREAM_START
        sta     zp_ptr_hi
        lda     #<STREAM_SIZE
        sta     zp_size_lo
        lda     #>STREAM_SIZE
        sta     zp_size_hi
        jsr     calc_checksum
        lda     zp_csum_lo
        cmp     selftest_stream_csum
        bne     @fail_stream
        lda     zp_csum_hi
        cmp     selftest_stream_csum+1
        bne     @fail_stream
        ldy     zp_copy_rem
        lda     #char_o
        sta     (zp_screen_lo),y
        iny
        lda     #char_k
        sta     (zp_screen_lo),y
        jmp     @done_stream
@fail_stream:
        ldy     zp_copy_rem
        jsr     print_hex_word
@done_stream:
        lda     zp_screen_lo
        clc
        adc     #40
        sta     zp_screen_lo
        bcc     @nc_s
        inc     zp_screen_hi
@nc_s:  rts

; ----------------------------------------------------------------------------
clear_screen:
        lda     #$00
        sta     $8C
        sta     $8E
        lda     #$04
        sta     $8D
        lda     #$D8
        sta     $8F
        ldy     #$00

; ----------------------------------------------------------------------------
L90F:
        lda     #$20
        sta     ($8C),y
        lda     #$00
        sec
        adc     $8C
        sta     $8C
        lda     #$00
        adc     $8D
        sta     $8D
        lda     #$00
        sec
        adc     $8E
        sta     $8E
        lda     #$00
        adc     $8F
        sta     $8F
        lda     #$E8
        cmp     $8C
        bne     L90F
        lda     #$07
        cmp     $8D
        bne     L90F
        rts

; ----------------------------------------------------------------------------
print_msg:
        lda     $8C
        pha
        lda     $8D
        pha
        jsr     clear_screen
        pla
        sta     $8D
        pla
        sta     $8C

; ----------------------------------------------------------------------------
print_string:
        lda     #$00
        sta     $8E
        lda     #$04
        sta     $8F
        lda     #$28
        sta     $7A
        ldy     #$00
print_loop:
        lda     ($8C),y
        beq     print_done
        cmp     #$0D
        bne     L977
        lda     $7A
        clc
        adc     $8E
        sta     $8E
        lda     #$00
        adc     $8F
        sta     $8F
        lda     #$28
        sta     $7A
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
        sta     ($8E),y
        dec     $7A
        bne     L997
        lda     #$28
        sta     $7A

; ----------------------------------------------------------------------------
L997:
        lda     #$00
        sec
        adc     $8E
        sta     $8E
        lda     #$00
        adc     $8F
        sta     $8F
advance_src:
        lda     #$00
        sec
        adc     $8C
        sta     $8C
        lda     #$00
        adc     $8D
        sta     $8D
        jmp     print_loop

; ----------------------------------------------------------------------------
print_done:
        rts

; ----------------------------------------------------------------------------
load_and_init:
        lda     zp_part_num
        cmp     #$07                ; Stop at song 7 (limited disk space)
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
        bcs     load_error
        lda     #$A1
        sta     $0427
        lda     zp_part_num
        and     #$01
        bne     init_buf2

; ----------------------------------------------------------------------------
; init_buf1: Called for EVEN part num (2,4,6,8) → even songs to TUNE2
init_buf1:
        lda     #$00
        ldx     #>TUNE2_BASE
        jsr     player_init
load_done:
        rts

; ----------------------------------------------------------------------------
; init_buf2: Called for ODD part num (1,3,5,7,9) → odd songs to TUNE1
init_buf2:
        lda     #$00
        ldx     #>TUNE1_BASE
        jsr     player_init
        jmp     load_done

; ----------------------------------------------------------------------------
load_error:
        lda     #<msg_error
        sta     $8C
        lda     #>msg_error
        sta     $8D
        jsr     print_msg
        lda     zp_part_num
        asl     a
        tax
        dex
        dex
        lda     #$FF
        sta     part_times,x
        jmp     load_done

; ----------------------------------------------------------------------
; Message text
; ----------------------------------------------------------------------
msg_loading:
        .byte   $00                     ; End of string
msg_error:
        .byte   $00                     ; End of string
msg_title:
        .byte   $00

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
load_tune:
        jmp     load_tune_impl

; ----------------------------------------------------------------------------
; Initialize stream pointer for in-memory decompression
; ----------------------------------------------------------------------------
init_stream:
        lda     #<STREAM_START
        sta     zp_src_lo
        lda     #>STREAM_START
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
        lda     #>TUNE1_BASE        ; Odd part num (1,3,5,7,9) -> TUNE1
        bne     @do_decompress
@even_part:
        lda     #>TUNE2_BASE        ; Even part num (2,4,6,8) -> TUNE2
@do_decompress:
        sta     zp_out_hi
        ; Call decompressor (reads from zp_src, writes to zp_out)
        ; Decompressor preserves zp_src and zp_bitbuf state for next call
        jsr     decompress
        clc                         ; Success
        rts

; ============================================================================
; V23 Decompressor - generated by ./compress
; Setup: zp_src, zp_bitbuf=$80, zp_out ($1800 or $6800)
; ============================================================================
checkpoint:
        rts
.include "../generated/decompress.asm"

; ============================================================================
; Standalone player (new format)
; ============================================================================
.include "odin_player.inc"

; ----------------------------------------------------------------------------
; ----------------------------------------------------------------------------
init:
        lda     #$00
        sta     zp_selected                 ; Auto-select part 1 (skip menu)
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

; ============================================================================
; SELFTEST - Decompress all songs and verify checksums
; ============================================================================

; Expected checksums - auto-generated by compress tool
.include "../generated/selftest_checksums.inc"

; Screen codes for display
char_0          = $30
char_s          = $13               ; S in screen code
char_colon      = $3A
char_space      = $20
char_p          = $10               ; P
char_a          = $01               ; A
char_f          = $06               ; F
char_i          = $09               ; I
char_l          = $0C               ; L
char_o          = $0F               ; O
char_k          = $0B               ; K
char_m          = $0D               ; M
char_t          = $14               ; T
char_r          = $12               ; R
char_e          = $05               ; E
char_n          = $0E               ; N

; ----------------------------------------------------------------------
; Selftest entry point
; ----------------------------------------------------------------------
selftest:
        jsr     clear_screen

        ; Display header "SELFTEST"
        lda     #<$0400
        sta     zp_screen_lo
        lda     #>$0400
        sta     zp_screen_hi
        ldy     #0
        lda     #char_s
        sta     (zp_screen_lo),y
        iny
        lda     #char_e
        sta     (zp_screen_lo),y
        iny
        lda     #char_l
        sta     (zp_screen_lo),y
        iny
        lda     #char_f
        sta     (zp_screen_lo),y
        iny
        lda     #char_t
        sta     (zp_screen_lo),y
        iny
        lda     #char_e
        sta     (zp_screen_lo),y
        iny
        lda     #char_s
        sta     (zp_screen_lo),y
        iny
        lda     #char_t
        sta     (zp_screen_lo),y

        ; Set screen position for stream results (row 2)
        lda     #<($0400 + 80)
        sta     zp_screen_lo
        lda     #>($0400 + 80)
        sta     zp_screen_hi

        ; Verify stream checksum
        jsr     selftest_verify_stream

        ; Set screen position for song results (row 4)
        lda     #<($0400 + 160)
        sta     zp_screen_lo
        lda     #>($0400 + 160)
        sta     zp_screen_hi

        ; Test all 9 songs
        lda     #0
        sta     zp_song_idx
        jsr     init_stream             ; Reset stream pointer for decompression tests

@song_loop:
        ; Display "Sx:" where x is song number
        ldy     #0
        lda     #char_s
        sta     (zp_screen_lo),y
        iny
        lda     zp_song_idx
        clc
        adc     #$31                ; '1' + song index
        sta     (zp_screen_lo),y
        iny
        lda     #char_colon
        sta     (zp_screen_lo),y
        iny

        ; Save Y for result position
        sty     zp_copy_rem

        ; Set output destination based on song index
        ; Even index (0,2,4,6,8) = odd songs (1,3,5,7,9) -> $1800
        ; Odd index (1,3,5,7) = even songs (2,4,6,8) -> $6800
        lda     #$00
        sta     zp_out_lo
        lda     zp_song_idx
        and     #$01
        bne     @odd_idx
        lda     #>TUNE1_BASE        ; Even index -> odd songs -> TUNE1
        bne     @set_dest
@odd_idx:
        lda     #>TUNE2_BASE        ; Odd index -> even songs -> TUNE2
@set_dest:
        sta     zp_out_hi

        ; Decompress (single stream, no split)
        jsr     decompress

        ; Calculate checksum of output
        jsr     selftest_output_checksum

        ; Compare with expected
        ldx     zp_song_idx
        txa
        asl     a
        tax
        lda     zp_csum_lo
        cmp     selftest_checksums,x
        bne     @fail
        lda     zp_csum_hi
        cmp     selftest_checksums+1,x
        bne     @fail

        ; Init the player (checksum passed, code is valid)
        lda     zp_song_idx
        and     #$01
        bne     @init_odd
        ldx     #>TUNE1_BASE        ; Even index -> odd songs -> buffer TUNE1
        bne     @init_player
@init_odd:
        ldx     #>TUNE2_BASE        ; Odd index -> even songs -> buffer TUNE2
@init_player:
        lda     #$00
        jsr     player_init

        lda     zp_song_idx
        clc
        adc     #$01
        sta     zp_part_num

        ; PASS - display OK and store $00
        ldy     zp_copy_rem
        lda     #char_o
        sta     (zp_screen_lo),y
        iny
        lda     #char_k
        sta     (zp_screen_lo),y
        ldx     zp_song_idx
        lda     #$00
        sta     $0801,x
        jmp     @next_song

@fail:
        ; FAIL - display checksum and store $FF
        ldy     zp_copy_rem
        jsr     print_hex_word
        ldx     zp_song_idx
        lda     #$FF
        sta     $0801,x

@next_song:
        ; Advance screen position (40 chars per row)
        lda     zp_screen_lo
        clc
        adc     #40
        sta     zp_screen_lo
        bcc     @no_carry
        inc     zp_screen_hi
@no_carry:
        inc     zp_song_idx
        lda     zp_song_idx
        cmp     #9
        beq     @done
        jmp     @song_loop

        ; Done - loop forever
@done:
        jmp     @done

.include "stream.inc"
