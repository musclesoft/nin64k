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
;   $1000 - Buffer 1 (odd parts: 1,3,5,7,9)
;   $7000 - Buffer 2 (even parts: 2,4,6,8)
;
; ============================================================================

.setcpu "6502"

; Zero page
zp_selected     = $78
zp_load_flag    = $79
zp_part_num     = $7B
; Selftest zero page ($D9-$E6 to avoid player's $FB-$FE)
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

; Tune entry points (jump vectors copied here)
TUNE1_BASE      = $1000
TUNE1_INIT      = $1000
TUNE1_PLAY      = $1003
TUNE2_BASE      = $7000
TUNE2_INIT      = $7000
TUNE2_PLAY      = $7003

; Player code starts at $1009/$7009, data at $198C/$798C
TUNE1_PLAYER    = $1009
TUNE1_DATA      = $198C
TUNE2_PLAYER    = $7009
TUNE2_DATA      = $798C

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

        lda     #$30
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
        lda     $01
        pha
        txa
        pha
        tya
        pha
        lda     #$35                ; Ensure I/O visible
        sta     $01

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
        sta     $01
        pla
        rts

; ----------------------------------------------------------------------------
play_tick:
        lda     zp_part_num
        and     #$01
        beq     play_buffer_b
        jsr     $1003
        jmp     check_countdown

; ----------------------------------------------------------------------------
play_buffer_b:
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
        lda     #$10
        bne     @out_set
@out_odd:
        lda     #$70
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
; Verify stream_main checksum (placed here to stay below $1000)
; ----------------------------------------------------------------------
selftest_verify_main:
        ldy     #0
        lda     #char_m
        sta     (zp_screen_lo),y
        iny
        lda     #char_a
        sta     (zp_screen_lo),y
        iny
        lda     #char_i
        sta     (zp_screen_lo),y
        iny
        lda     #char_n
        sta     (zp_screen_lo),y
        iny
        lda     #char_colon
        sta     (zp_screen_lo),y
        iny
        sty     zp_copy_rem
        lda     #<STREAM_MAIN_DEST
        sta     zp_ptr_lo
        lda     #>STREAM_MAIN_DEST
        sta     zp_ptr_hi
        lda     #<STREAM_MAIN_SIZE
        sta     zp_size_lo
        lda     #>STREAM_MAIN_SIZE
        sta     zp_size_hi
        jsr     calc_checksum
        lda     zp_csum_lo
        cmp     selftest_stream_main_csum
        bne     @fail_main
        lda     zp_csum_hi
        cmp     selftest_stream_main_csum+1
        bne     @fail_main
        ldy     zp_copy_rem
        lda     #char_o
        sta     (zp_screen_lo),y
        iny
        lda     #char_k
        sta     (zp_screen_lo),y
        jmp     @done_main
@fail_main:
        ldy     zp_copy_rem
        jsr     print_hex_word
@done_main:
        lda     zp_screen_lo
        clc
        adc     #40
        sta     zp_screen_lo
        bcc     @nc_m
        inc     zp_screen_hi
@nc_m:  rts

; ----------------------------------------------------------------------
; Verify stream_tail checksum
; ----------------------------------------------------------------------
selftest_verify_tail:
        ldy     #0
        lda     #char_t
        sta     (zp_screen_lo),y
        iny
        lda     #char_a
        sta     (zp_screen_lo),y
        iny
        lda     #char_i
        sta     (zp_screen_lo),y
        iny
        lda     #char_l
        sta     (zp_screen_lo),y
        iny
        lda     #char_colon
        sta     (zp_screen_lo),y
        iny
        sty     zp_copy_rem
        lda     #<STREAM_TAIL_DEST
        sta     zp_ptr_lo
        lda     #>STREAM_TAIL_DEST
        sta     zp_ptr_hi
        lda     #<STREAM_TAIL_SIZE
        sta     zp_size_lo
        lda     #>STREAM_TAIL_SIZE
        sta     zp_size_hi
        sei
        jsr     calc_checksum
        cli
        lda     zp_csum_lo
        cmp     selftest_stream_tail_csum
        bne     @fail_tail
        lda     zp_csum_hi
        cmp     selftest_stream_tail_csum+1
        bne     @fail_tail
        ldy     zp_copy_rem
        lda     #char_o
        sta     (zp_screen_lo),y
        iny
        lda     #char_k
        sta     (zp_screen_lo),y
        jmp     @done_tail
@fail_tail:
        ldy     zp_copy_rem
        jsr     print_hex_word
@done_tail:
        lda     zp_screen_lo
        clc
        adc     #40
        sta     zp_screen_lo
        bcc     @nc_t
        inc     zp_screen_hi
@nc_t:  rts

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
; init_buf1: Called for EVEN $7B (0,2,4,6,8) → loads ODD files to $1000
init_buf1:
        lda     #$60
        sta     $105C               ; Patch stop routine
        lda     #$00
        jsr     TUNE1_INIT
load_done:
        rts

; ----------------------------------------------------------------------------
; init_buf2: Called for ODD $7B (1,3,5,7) → loads EVEN files to $7000
init_buf2:
        lda     #$60
        sta     $705C               ; Patch stop routine
        lda     #$00
        jsr     TUNE2_INIT
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
        ; Call decompressor (reads from zp_src, writes to zp_out)
        ; Decompressor preserves zp_src and zp_bitbuf state for next call
        jsr     decompress
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
init:
        jsr     copy_streams        ; Copy compressed data to safe location

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

; Expected checksums for decompressed songs 1-9 (16-bit additive)
selftest_checksums:
        .word   $4541               ; Song 1
        .word   $A9C7               ; Song 2
        .word   $656A               ; Song 3
        .word   $01F5               ; Song 4
        .word   $D543               ; Song 5
        .word   $8757               ; Song 6
        .word   $8831               ; Song 7
        .word   $D3DB               ; Song 8
        .word   $4724               ; Song 9

; Song sizes in bytes (songs 1-9)
selftest_sizes:
        .word   21085               ; Song 1
        .word   21375               ; Song 2
        .word   19464               ; Song 3
        .word   22889               ; Song 4
        .word   22075               ; Song 5
        .word   20300               ; Song 6
        .word   14423               ; Song 7
        .word   20707               ; Song 8
        .word   21620               ; Song 9

; Expected stream checksums
selftest_stream_main_csum:  .word $FFE8
selftest_stream_tail_csum:  .word $4D99

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

        ; Verify stream_main checksum
        jsr     selftest_verify_main
        ; Verify stream_tail checksum
        jsr     selftest_verify_tail

        ; Set screen position for song results (row 4)
        lda     #<($0400 + 160)
        sta     zp_screen_lo
        lda     #>($0400 + 160)
        sta     zp_screen_hi

        ; Test all 9 songs
        lda     #0
        sta     zp_song_idx

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
        lda     #$00
        sta     zp_out_lo
        lda     zp_song_idx
        and     #$01
        bne     @odd_song
        lda     #$10                ; Even index (0,2,4,6,8) -> $1000
        bne     @set_dest
@odd_song:
        lda     #$70                ; Odd index (1,3,5,7) -> $7000
@set_dest:
        sta     zp_out_hi

        ; Decompress (stream spans $D000, need all-RAM mode)
        jsr     decompress
        ; S9 is split: stream_main has first part, stream_tail has rest
        lda     zp_song_idx
        cmp     #8                  ; Song 9 = index 8
        bne     @not_s9
        ; Switch to stream_tail and continue decompressing
        lda     #<STREAM_TAIL_DEST
        sta     zp_src_lo
        lda     #>STREAM_TAIL_DEST
        sta     zp_src_hi
        lda     #$80
        sta     zp_bitbuf
        jsr     decompress
@not_s9:

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
        lda     #$00
        jsr     TUNE1_INIT
        jmp     @init_done
@init_odd:
        lda     #$00
        jsr     TUNE2_INIT
@init_done:

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
