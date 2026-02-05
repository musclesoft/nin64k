; ============================================================================
; nin64sid - Minimal SID player for export
; ============================================================================
;
; SID header info:
;   Init: sid_init (sets up IRQ for continuous playback)
;   Play: internal IRQ
;   Songs: 1
;
; INIT: Call to start continuous playback of all 9 parts (sets up IRQ)
;
; ============================================================================

.setcpu "6502"

; Zero page (avoid player's $10-$21)
zp_part_num     = $7E
zp_preloaded    = $7F              ; Song number that's been preloaded
zp_last_line    = $0D

; Decompressor zero page (external interface)
zp_src_lo       = $02
zp_src_hi       = $03
zp_bitbuf       = $04
zp_out_lo       = $05
zp_out_hi       = $06

; Buffer addresses (must match DECOMP_BUF1_HI/DECOMP_BUF2_HI in decompress.asm)
TUNE1_BASE      = $2000            ; Odd songs (1,3,5,7,9)
TUNE2_BASE      = $4000            ; Even songs (2,4,6,8)

.segment "RSIDHEADER"
        .byte   "RSID"                  ; $00: Magic
        .word   $0200                   ; $04: Version (big-endian $0002)
        .word   $7C00                   ; $06: Data offset (big-endian $007C)
        .word   $0000                   ; $08: Load address (0 = in data, required for RSID)
        .byte   >sid_init, <sid_init    ; $0A: Init address (big-endian)
        .word   $0000                   ; $0C: Play address (0 = required for RSID)
        .word   $0100                   ; $0E: Songs (big-endian $0001 = 1)
        .word   $0100                   ; $10: Start song (big-endian $0001 = 1)
        .dword  $00000000               ; $12: Speed flags
        .byte   "Nine Inch Ninjas", 0   ; $16: Name (32 bytes)
        .res    32-17
        .byte   "Otto J", $e4, "rvinen (SounDemoN)", 0  ; $36: Author (32 bytes)
        .res    32-26
        .byte   "2000 SounDemoN", 0     ; $56: Released (32 bytes)
        .res    32-15
        .word   $3400                   ; $76: Flags (big-endian $0034 = PAL, 6581+8580)
        .word   $0000                   ; $78: No player relocation
        .word   $0000                   ; $7A: Reserved

.segment "LOADADDR"
        .word   sid_init                ; Load address embedded in data (required for RSID)

.segment "CODE"

; ----------------------------------------------------------------------------
; SID entry point - Initialize continuous playback from song 1, set up IRQ
; ----------------------------------------------------------------------------
sid_init:
        sei
        lda     #$35                ; I/O visible, BASIC banked out
        sta     $01
        jsr     setup_irq
        jsr     init_stream
        ; Init player for song 1
        lda     #1
        sta     zp_part_num
        sta     zp_preloaded
        lda     #$00
        ldx     #>TUNE1_BASE
        jsr     player_init
        cli                         ; Enable IRQ - music starts playing

; ----------------------------------------------------------------------------
; Main loop - just preloading, playback happens in IRQ
; ----------------------------------------------------------------------------
main_loop:
        lda     zp_part_num
        cmp     zp_preloaded
        bcc     main_loop           ; Already preloaded
        jsr     do_preload
        jmp     main_loop

; ----------------------------------------------------------------------------
; Set up raster IRQ
; ----------------------------------------------------------------------------
setup_irq:
        lda     #$7F
        sta     $DC0D
        lda     $DC0D
        lda     #$01
        sta     $D01A
        lda     #<irq_handler
        sta     $FFFE
        lda     #>irq_handler
        sta     $FFFF
        rts

; ----------------------------------------------------------------------------
; IRQ handler - called on vblank
; ----------------------------------------------------------------------------
irq_handler:
        pha
        txa
        pha
        tya
        pha
        lda     $D019
        sta     $D019
        jsr     player_play
        jsr     check_countdown
        pla
        tay
        pla
        tax
        pla
        rti

; ----------------------------------------------------------------------------
; Checkpoint - called during decompression, just returns (IRQ handles playback)
; ----------------------------------------------------------------------------
checkpoint:
        rts

; ----------------------------------------------------------------------------
; Preload next song (decompress to alternate buffer)
; ----------------------------------------------------------------------------
do_preload:
        ldx     zp_part_num
        cpx     #9
        bcs     @done               ; No preload after song 9
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
@done:
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

; ----------------------------------------------------------------------
; Part timing data (decremented in place during playback)
; ----------------------------------------------------------------------
part_times:
.include "part_times.inc"

; ----------------------------------------------------------------------------
; Initialize stream pointer to song 2
; Stream2 starts byte-aligned at song 2 (no bit offset needed)
; ----------------------------------------------------------------------------
init_stream:
        lda     #$80                    ; Empty buffer, will load on first read
        sta     zp_bitbuf
        lda     #<STREAM_START
        sta     zp_src_lo
        lda     #>STREAM_START
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
.incbin "../generated/parts/part1.bin"

.segment "STREAM"
USE_STREAM2 = 1                 ; Use stream2.bin (songs 2-9 only, byte-aligned)
.include "stream.inc"
