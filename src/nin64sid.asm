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

; Zero page
zp_part_num     = $7B
zp_preloaded    = $7C              ; Song number that's been preloaded

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
        jsr     setup_irq           ; Set up IRQ before stream copy overwrites $DC0D
        lda     #$30                ; All RAM for stream access
        sta     $01
        lda     #1
        sta     zp_part_num
        sta     zp_preloaded
        lda     #0
        jsr     TUNE1_INIT
        cli
        jsr     copy_streams
        jsr     init_stream
@loop:
        lda     zp_part_num
        cmp     zp_preloaded
        bcc     @loop               ; Already preloaded
        jsr     do_preload          ; Preload next song
        jmp     @loop

; ----------------------------------------------------------------------------
; Set up raster IRQ
; ----------------------------------------------------------------------------
setup_irq:
        lda     #$35                ; I/O visible for VIC/CIA setup
        sta     $01
        lda     #$7F
        sta     $DC0D
        lda     $DC0D
        lda     #$01
        sta     $D01A
        lda     #<irq_handler
        sta     $FFFE
        lda     #>irq_handler
        sta     $FFFF
        lda     #$30                ; Restore $01 for main loop
        sta     $01
        rts

; ----------------------------------------------------------------------------
; IRQ handler
; ----------------------------------------------------------------------------
irq_handler:
        pha
        lda     $01
        pha
        txa
        pha
        tya
        pha
        lda     #$35                ; I/O visible for SID
        sta     $01
        lda     $D019
        sta     $D019
        jsr     do_play
        pla
        tay
        pla
        tax
        pla
        sta     $01                 ; Restore RAM config
        pla
        rti

; ----------------------------------------------------------------------------
; PLAY - Call once per frame
; ----------------------------------------------------------------------------
do_play:
        lda     zp_part_num
        beq     @done
        and     #$01
        beq     @play_even
        jsr     TUNE1_PLAY
        jmp     @after_play
@play_even:
        jsr     TUNE2_PLAY
@after_play:
        jsr     check_countdown
@done:
        rts

; ----------------------------------------------------------------------------
; do_preload - Decompress and init next song (like nin64k's load_and_init)
; ----------------------------------------------------------------------------
do_preload:
        ldx     zp_part_num
        cpx     #9
        bcs     @done               ; No preload after song 9
        inx                         ; Next song number
        txa
        pha                         ; Save next song number
        jsr     decompress_one      ; Decompress next song
        ; Init the decompressed song ($01=$30 from main loop, tune data spans $A000+)
        lda     zp_part_num
        and     #$01
        bne     @init_even          ; Current odd -> next even -> $7000
        ; Current even -> next odd -> $1000
        lda     #$00
        jsr     TUNE1_INIT
        jmp     @restore
@init_even:
        lda     #$00
        jsr     TUNE2_INIT
@restore:
        pla                         ; Recover next song number
        sta     zp_preloaded        ; Mark as preloaded (after decompress+init done)
@done:
        rts

; ----------------------------------------------------------------------------
; check_countdown - Decrement timer, switch songs when expired
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
        bne     @not_underflow
        dec     part_times+1,x
@not_underflow:
        lda     part_times,x
        bne     @done
        lda     part_times+1,x
        bne     @done
        lda     zp_part_num
        cmp     #$09
        bcs     @done               ; Don't advance past song 9
        ; Song ended - switch to preloaded song (preload happens next frame)
        inc     zp_part_num
@done:
        rts

; ----------------------------------------------------------------------------
; decompress_one - Decompress song X (1-9)
; Song 9 is split: first part in stream_main, rest in stream_tail
; ----------------------------------------------------------------------------
decompress_one:
        txa
        pha                         ; Save song number
        lda     #$00
        sta     zp_out_lo
        txa
        and     #$01
        bne     @odd
        lda     #$70
        bne     @set
@odd:
        lda     #$10
@set:
        sta     zp_out_hi
        jsr     decompress
        pla                         ; Get song number
        cmp     #9
        bne     @done
        pha                         ; Save for return
        lda     #<STREAM_TAIL_DEST
        sta     zp_src_lo
        lda     #>STREAM_TAIL_DEST
        sta     zp_src_hi
        lda     #$80
        sta     zp_bitbuf
        jsr     decompress
        pla
@done:
        tax                         ; Restore X
        rts

; ----------------------------------------------------------------------
; Part timing data (decremented in place during playback)
; ----------------------------------------------------------------------
part_times:
.include "part_times.inc"

; ----------------------------------------------------------------------------
; init_stream - Initialize stream pointer to song 2 (song 1 is preloaded)
; Song 1 = 39981 bits = 4997 bytes + 5 bits, so song 2 starts mid-byte
; ----------------------------------------------------------------------------
STREAM_OFFSET = 4997                    ; Byte offset where song 2 starts

init_stream:
        lda     #<(STREAM_MAIN_DEST + 1)
        sta     zp_src_lo
        lda     #>(STREAM_MAIN_DEST + 1)
        sta     zp_src_hi
        lda     STREAM_MAIN_DEST        ; Load partial byte
        asl     a                       ; Shift out 5 bits consumed by song 1
        asl     a
        asl     a
        asl     a
        asl     a
        ora     #$10                    ; Add sentinel (3 bits remain at 7,6,5)
        sta     zp_bitbuf
        rts

; ============================================================================
; Decompressor
; ============================================================================
.include "../generated/decompress.asm"

.segment "PART1"
.incbin "../generated/part1.bin"

.segment "DATA"
.include "stream.inc"
