; ============================================================================
; Nine Inch Ninjas - Standalone PRG player
; ============================================================================

.setcpu "6502"

; Zero page
zp_part_num     = $7B
zp_preloaded    = $7C              ; Song number that's been preloaded
zp_last_line    = $0D

; Decompressor zero page (external interface)
zp_src_lo       = $02
zp_src_hi       = $03
zp_bitbuf       = $04
zp_out_lo       = $05
zp_out_hi       = $06

; Buffer addresses (new format)
TUNE1_BASE      = $1800         ; Odd songs (1,3,5,7,9)
TUNE2_BASE      = $6800         ; Even songs (2,4,6,8)

.segment "LOADADDR"
        .word   $0801

.segment "CODE"

; ----------------------------------------------------------------------
; BASIC stub: SYS 2061
; ----------------------------------------------------------------------
basic_stub:
        .word   $0810               ; Pointer to next BASIC line
        .word   8580                ; Line number
        .byte   $9E                 ; SYS token
        .byte   "2061"              ; SYS address
        .byte   $00                 ; End of line
        .word   $0000               ; End of BASIC program

; ----------------------------------------------------------------------------
start:
        sei
        ; Set up safe IRQ vector for all-RAM mode
        lda     #<safe_rti
        sta     $FFFE
        lda     #>safe_rti
        sta     $FFFF
        ; Disable CIA interrupts
        lda     #$7F
        sta     $DC0D
        lda     $DC0D

        ; Switch to all-RAM for stream copy
        lda     #$30
        sta     $01
        jsr     copy_streams
        jsr     init_stream

        ; Init player for song 1
        lda     #1
        sta     zp_part_num
        sta     zp_preloaded
        lda     #$00
        ldx     #>TUNE1_BASE
        jsr     player_init

        cli

; ----------------------------------------------------------------------------
; Main loop - polling-based playback with preloading
; ----------------------------------------------------------------------------
main_loop:
        jsr     checkpoint
        lda     #$35
        sta     $01
        ; Check space bar for skip
        lda     #$7F                ; Select keyboard row 7
        sta     $DC00
        lda     $DC01
        and     #$10                ; Check bit 4 (space bar)
        bne     @check_preload      ; Not pressed
        ; Space pressed - debounce
@debounce:
        jsr     checkpoint
        lda     #$35
        sta     $01
        lda     $DC01
        and     #$10
        beq     @debounce           ; Wait for release
        ; Reset SID
        ldx     #$18
        lda     #$00
@sid:   sta     $D400,x
        dex
        bpl     @sid
        ; Skip to next song if preloaded
        lda     zp_part_num
        cmp     #$09
        beq     @check_preload      ; Already at song 9
        cmp     zp_preloaded
        bcs     @check_preload      ; Next not preloaded yet
        ; Switch to preloaded song
        inc     zp_part_num
        lda     zp_part_num
        and     #$01
        bne     @init_odd
        ; Even part num (2,4,6,8) -> buffer at $6800
        lda     #$00
        ldx     #>TUNE2_BASE
        jsr     player_init
        jmp     @check_preload
@init_odd:
        ; Odd part num (3,5,7,9) -> buffer at $1800
        lda     #$00
        ldx     #>TUNE1_BASE
        jsr     player_init
@check_preload:
        lda     #$30
        sta     $01
        ; Check if preload needed
        lda     zp_part_num
        cmp     zp_preloaded
        bcc     main_loop           ; Already preloaded
        jsr     do_preload
        jmp     main_loop

safe_rti:
        rti

; ----------------------------------------------------------------------------
; Checkpoint - called during decompression for vblank detection and indicator
; ----------------------------------------------------------------------------
checkpoint:
        lda     #$35
        sta     $01
        rol     $D020               ; Loading indicator
        lda     $D012
        bmi     @no_vblank
        cmp     zp_last_line
        bpl     @no_vblank
        jsr     play_frame
        lda     #0
@no_vblank:
        sta     zp_last_line
        lda     #$30
        sta     $01
        rts

; ----------------------------------------------------------------------------
; Play one frame of music
; ----------------------------------------------------------------------------
play_frame:
        lda     #$07
        sta     $D020
        txa
        pha
        tya
        pha
        jsr     player_play
        jsr     check_countdown
        pla
        tay
        pla
        tax
        lda     #$00
        sta     $D020
        rts

; ----------------------------------------------------------------------------
; Check countdown timer, switch to preloaded song when expired
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
        bne     @check_zero
        dec     part_times+1,x
@check_zero:
        lda     part_times,x
        bne     @done
        lda     part_times+1,x
        bne     @done
        lda     zp_part_num
        cmp     #$09
        beq     @done
        ; Song ended - switch to preloaded song immediately
        inc     zp_part_num
        ; Init player for the preloaded buffer
        lda     zp_part_num
        and     #$01
        bne     @switch_odd
        ; Even part num (2,4,6,8) -> buffer at $6800
        lda     #$00
        ldx     #>TUNE2_BASE
        jsr     player_init
        jmp     @done
@switch_odd:
        ; Odd part num (3,5,7,9) -> buffer at $1800
        lda     #$00
        ldx     #>TUNE1_BASE
        jsr     player_init
@done:
        rts

; ----------------------------------------------------------------------------
; Preload next song (decompress to alternate buffer)
; ----------------------------------------------------------------------------
do_preload:
        ldx     zp_part_num
        cpx     #9
        bcs     @done               ; No preload after song 9
        lda     #$CC                ; Loading indicator on screen
        sta     $0427
        inx                         ; Next song number
        txa
        pha                         ; Save next song number
        ; Decompress to alternate buffer
        lda     #$00
        sta     zp_out_lo
        txa
        and     #$01
        bne     @preload_odd
        ; Even song -> $6800
        lda     #>TUNE2_BASE
        bne     @decompress
@preload_odd:
        ; Odd song -> $1800
        lda     #>TUNE1_BASE
@decompress:
        sta     zp_out_hi
        jsr     decompress
        pla                         ; Recover next song number
        sta     zp_preloaded        ; Mark as preloaded
        lda     #$20                ; Clear loading indicator
        sta     $0427
@done:
        rts

; ----------------------------------------------------------------------------
; Part timing data
; ----------------------------------------------------------------------------
part_times:
.include "part_times.inc"

; ----------------------------------------------------------------------------
; Initialize stream pointer to song 2
; Stream is included with STREAM_OFFSET=3026 so byte 0 = original byte 3026
; Song 1 = 24210 bits = byte 3026, bit 2 of original stream
; ----------------------------------------------------------------------------
init_stream:
        lda     STREAM_DEST             ; First byte of offset stream
        sec
        rol     a
        asl     a
        sta     zp_bitbuf
        lda     #<(STREAM_DEST + 1)
        sta     zp_src_lo
        lda     #>(STREAM_DEST + 1)
        sta     zp_src_hi
        rts

; ============================================================================
; Decompressor (calls checkpoint for vblank detection during decompression)
; ============================================================================
.include "../generated/decompress.asm"

; ============================================================================
; Standalone player (new format)
; ============================================================================
.include "odin_player.inc"

.segment "PART1"
.incbin "../generated/part1.bin"

.segment "DATA"
STREAM_OFFSET = 3026            ; Skip song 1's compressed data (pre-loaded from part1.bin)
.include "stream.inc"
