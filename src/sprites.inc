.setcpu "6502"

sprites_init:
        lda     #$00
        sta     $D015           ; Enable
        sta     $D01D           ; X expand
        lda     #$ff
        sta     $D017           ; Y expand

        ; Set X positions (low byte)
        sec
        lda     #$ff
        sta     $D000
        sbc     #$10
        sta     $D002
        sbc     #$10
        sta     $D004
        sbc     #$10
        sta     $D006
        sbc     #$10
        sta     $D008
        sbc     #$10
        sta     $D00A
        sbc     #$10
        sta     $D00C
        sbc     #$10
        sta     $D00E

        ; X MSB
        lda     #0
        sta     $D010

        ; Set Y positions
        lda     #100
        sta     $D001
        sta     $D003
        sta     $D005
        sta     $D007
        sta     $D009
        sta     $D00B
        sta     $D00D
        sta     $D00F

        ; Fill sprite data at $0340 (pointer 13)
        lda     #$FF
        ldx     #62
@fill:
        sta     $0340,x
        dex
        bpl     @fill

        ; Set sprite pointers to 13
        lda     #13
        ldx     #7
@ptr:
        sta     $07F8,x
        dex
        bpl     @ptr

        ; Set sprite colors (1-8)
        ldx     #7
@color:
        inx
        txa
        dex
        sta     $D027,x
        dex
        bpl     @color

        rts

sprites_ypos:
        .res     4, 21 *  0
        .res    21, 21 *  0 + 21
        .res    21, 21 *  1 + 21
        .res    21, 21 *  2 + 21
        .res    21, 21 *  3 + 21
        .res    21, 21 *  4 + 21
        .res    21, 21 *  5 + 21
        .res    21, 21 *  6 + 21
        .res    21, 21 *  7 + 21
        .res    21, 21 *  8 + 21
        .res    21, 21 *  9 + 21
        .res    21, 21 *  9 + 21
        .res    21, 21 *  9 + 21
        .res    21, 21 *  6
