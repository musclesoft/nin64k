; ============================================================================
; Odin Player - New Format Version for ca65
; ============================================================================
; Entry points:
;   +$00: Init - A=song number (ignored), X=buffer page high byte (e.g. $10, $70)
;   +$03: Play - call once per frame
; ============================================================================

; Entry point jump table
        jmp     player_init             ; +$00: Init (A=song, X=buffer)
        jmp     player_play             ; +$03: Play one frame

; Zero page usage (contiguous block $70-$7D)
zp_song_base     = $70          ; 2 bytes: base address of current song data
zp_ptr           = $72          ; 2 bytes: temp pointer / temp values
zp_trackptr      = $74          ; 2 bytes: track pointer
zp_row_dict      = $76          ; 2 bytes: row dictionary address
zp_packed_data   = $78          ; 2 bytes: packed data address
zp_decode_src    = $7A          ; 2 bytes: decode source pointer (temp)
zp_decode_dst    = $7C          ; 2 bytes: decode dest pointer (temp)

; Song format: fixed offsets (all tables deduplicated)
; $000: Instruments (512 bytes) - interleaved: inst N at offset N*16
; $200: Transpose ch0-2 (256 bytes each)
; $500: Trackptr ch0-2 (256 bytes each)
; $800: Filtertable (234 bytes max)
; $8EA: Arptable (188 bytes max)
; $9A6: Row dictionary (1236 bytes = 412 entries × 3)
; $E7A: Packed pointers (182 bytes = 91 patterns × 2)
; $F30: Packed pattern data
;
; Global wavetable (163 bytes) is stored in player, shared across all songs.
; Instrument wave indices point directly into global_wavetable.
;
; Pattern packing format:
;   Row dictionary: 3 bytes per entry [note, inst|effect, param], sorted by frequency
;   Packed pointers: 2-byte offset into packed data for each pattern
;   Packed data encoding:
;     $00-$DF: dictionary index 0-223
;     $E0-$FE: RLE repeat previous row 1-31 times
;     $FF + byte: extended dictionary index (224 + byte)
;   Each pattern decodes to 64 rows × 3 bytes = 192 bytes
;
; Row format (3 bytes):
;   Byte 0 (Note):    e nnn nnnn    e=effect bit 3, n=note value (0-96, $61=off)
;   Byte 1 (InstEff): EEE iiiii    E=effect bits 0-2, i=instrument (0-31)
;   Byte 2 (Param):   pppp pppp    p=effect parameter
;
;   Note values: $00=none, $01-$60=note 0-95 (subtract 1), $61=note-off
;   Effect = (InstEff >> 5) | ((Note >> 4) & $08)
;
; Effect mapping (frequency-sorted):
;   0=special (param: 0=nop, 1=vib off, 2=break, 3=fineslide)
;   1=arp 2=porta 3=speed 4=hrdrest 5=filttrig 6=SR 7=wave
;   8=pulse 9=AD A=reso B=slide C=globalvol D=filtmode E=jump
;
; Example: $00 $20 $49
;   Note=$00:    note = $00 & $7F = 0 (none), effect bit 3 = 0
;   InstEff=$20: inst = 0, effect bits 0-2 = 1
;   Param=$49:   parameter = $49
;   -> effect = (1<<5>>5) | (($00>>4)&8) = 1 | 0 = 1 (arpeggio $49)

; Pattern packing constants
ROW_DICT_OFF        = $9A6
PACKED_PTRS_OFF     = $E7A
PACKED_DATA_OFF     = $F30

; Instrument parameter offsets within 16-byte instrument block
INST_AD          = 0
INST_SR          = 1
INST_WAVESTART   = 2
INST_WAVEEND     = 3
INST_WAVELOOP    = 4
INST_ARPSTART    = 5
INST_ARPEND      = 6
INST_ARPLOOP     = 7
INST_VIBDELAY    = 8
INST_VIBDEPSP    = 9
INST_PULSEWIDTH  = 10
INST_PULSESPEED  = 11
INST_PULSELIMITS = 12
INST_FILTSTART   = 13
INST_FILTEND     = 14
INST_FILTLOOP    = 15

; ============================================================================
; player_init - A=song number (ignored), X=buffer page high byte
; ============================================================================
player_init:
        stx     zp_song_base+1
        ; Calculate transpose table pages ($200, $300, $400 from base)
        inx
        inx                             ; X = base + 2
        stx     transpose_page+0
        inx                             ; X = base + 3
        stx     transpose_page+1
        inx                             ; X = base + 4
        stx     transpose_page+2
        ; Calculate trackptr table pages ($500, $600, $700 from base)
        inx                             ; X = base + 5
        stx     trackptr_page+0
        inx                             ; X = base + 6
        stx     trackptr_page+1
        inx                             ; X = base + 7
        stx     trackptr_page+2
        ; Patch table addresses (SMC)
        inx                             ; X = base + 8
        stx     filter_load+2           ; $800 high byte
        stx     arp_load_addr+1         ; $8EA high byte (same page)
        lda     #$ea
        sta     arp_load_addr
        lda     #$00
        sta     filter_load+1
        sta     zp_song_base
        ; Clear player variables
        tay
@clear:
        sta     chn_hardrestart,y
        iny
        cpy     #PLAYERVARSSIZE
        bne     @clear

        ; Initialize playback state (always start at order 0)
        lda     #$80
        sta     forcenewpattern
        lda     #0
        sta     ordernumber
        sta     nextordernumber
        ldy     #6
        sty     speed
        dey
        sty     speedcounter
        lda     #2
        sta     chn_hardrestart+0
        sta     chn_hardrestart+1
        sta     chn_hardrestart+2
        ; Initialize inst pointers to instrument 0 (at song_base)
        lda     zp_song_base+1
        sta     chn_inst_ptr_hi+0
        sta     chn_inst_ptr_hi+1
        sta     chn_inst_ptr_hi+2
        lda     #$0f
        sta     globalvolume
        ; Initialize decode buffer state (mark all as empty)
        lda     #$FF
        sta     chn_decoded_pat+0
        sta     chn_decoded_pat+1
        sta     chn_decoded_pat+2
        sta     chn_decoded_row+0
        sta     chn_decoded_row+1
        sta     chn_decoded_row+2
        ; Set row_dict_addr = song_base + $9D9
        lda     zp_song_base
        clc
        adc     #<ROW_DICT_OFF
        sta     zp_row_dict
        lda     zp_song_base+1
        adc     #>ROW_DICT_OFF
        sta     zp_row_dict+1
        ; Set packed_ptrs_addr = song_base + $EAD
        lda     zp_song_base
        clc
        adc     #<PACKED_PTRS_OFF
        sta     packed_ptrs_addr
        lda     zp_song_base+1
        adc     #>PACKED_PTRS_OFF
        sta     packed_ptrs_addr+1
        ; Set packed_data = song_base + $F63
        lda     zp_song_base
        clc
        adc     #<PACKED_DATA_OFF
        sta     zp_packed_data
        lda     zp_song_base+1
        adc     #>PACKED_DATA_OFF
        sta     zp_packed_data+1
        rts

; ============================================================================
; player_play - Play one frame
; ============================================================================
player_play:
        dec     mod3counter
        bpl     @mod3ok
        lda     #$02
        sta     mod3counter
@mod3ok:
        inc     speedcounter
        lda     speedcounter
        cmp     speed
        bne     @skip_pattern
        jmp     @pattern_update
@skip_pattern:
        jmp     @allframes
@pattern_update:
        lda     #$00
        sta     speedcounter
        lda     forcenewpattern
        bmi     @newpattern
        inc     trackrow
        lda     trackrow
        cmp     #$40
        bmi     @pattern_ok
@newpattern:
        lda     nextordernumber
        sta     ordernumber
        inc     nextordernumber
        lda     #$00
        sta     trackrow
        sta     forcenewpattern
@pattern_ok:
        lda     trackrow
        sta     decode_row              ; set decode_row for get_trackptr_order
        and     #$0f            ; row_in_quarter = trackrow & 15
        sta     trackrow3       ; temp storage
        asl     a               ; A = row_in_quarter * 2
        adc     trackrow3       ; A = row_in_quarter * 3
        sta     trackrow3
        ldx     ordernumber
        ldy     #0
        jsr     get_transpose_order
        sta     chn_transpose+0
        ldy     #0
        jsr     get_trackptr_order
        ldx     #0
        jsr     fetch_channel_row
        ldx     ordernumber
        ldy     #1
        jsr     get_transpose_order
        sta     chn_transpose+1
        ldy     #1
        jsr     get_trackptr_order
        ldx     #1
        jsr     fetch_channel_row
        ldx     ordernumber
        ldy     #2
        jsr     get_transpose_order
        sta     chn_transpose+2
        ldy     #2
        jsr     get_trackptr_order
        ldx     #2
        jsr     fetch_channel_row

@allframes:
        ldx     #$00
@chnloop:
        lda     #$00
        sta     chn_vibdepth,x
        lda     chn_inst,x
        beq     @noinst
        jsr     process_instrument
@noinst:
        lda     chn_effect,x
        bne     @haseffect
        ; Effect 0: special handling based on param
        ; param 0=nop, 1=vib, 2=break, 3=fineslide
        lda     chn_effectpar,x
        bne     @effect0_dispatch
        jmp     @noeffect              ; param 0 = no effect
@effect0_dispatch:
        cmp     #2
        bcs     @effect0_break_or_fineslide
        ; param 1 = vib: disable vibrato
        lda     #0
        sta     chn_vibdepth,x
        sta     chn_vibspeed,x
        jmp     @noeffect
@effect0_break_or_fineslide:
        beq     @effect0_break
        ; param 3 = fineslide
        lda     speedcounter
        beq     @do_fineslide
        jmp     @noeffect
@do_fineslide:
        lda     chn_slidedelta_lo,x
        clc
        adc     #$04
        sta     chn_slidedelta_lo,x
        lda     #$80
        sta     chn_slideenable,x
        jmp     @noeffect
@effect0_break:
        ; param 2 = break
        lda     #$80
        sta     forcenewpattern
        jmp     @noeffect
@haseffect:
        ; Binary search dispatch for effects 1-E (14 effects), all inlined
        cmp     #8
        bcc     @fx_1_to_7
        jmp     @fx_8_to_e
@fx_1_to_7:
        cmp     #4
        bcc     @fx_1_to_3
        ; Effects 4-7 (fall through to save JMP)
        cmp     #6
        bcs     @fx_6_7
        cmp     #5
        bcs     @fx_5
        ; Effect 4 = hard restart (inlined)
        lda     speedcounter
        bne     @fx_exit2
        lda     chn_effectpar,x
        sta     chn_hardrestart,x
@fx_exit2:
        jmp     @noeffect
@fx_5:
        ; Effect 5 = filter trigger (inlined)
        lda     speedcounter
        bne     @fx_exit2
        lda     chn_effectpar,x
        beq     @fx_exit2
        asl
        asl
        asl
        asl
        sta     zp_ptr
        lda     #0
        rol
        adc     zp_song_base+1
        sta     zp_ptr+1
        ldy     #INST_FILTSTART
        lda     (zp_ptr),y
        sta     filter_idx
        ldy     #INST_FILTEND
        lda     (zp_ptr),y
        sta     filter_end
        ldy     #INST_FILTLOOP
        lda     (zp_ptr),y
        sta     filter_loop
        jmp     @noeffect
@fx_6_7:
        cmp     #7
        bcs     @fx_7
        ; Effect 6 = set SR (inlined)
        ldy     chn_effectpar,x
        lda     sr_remap,y
        sta     chn_sr,x
        jmp     @noeffect
@fx_7:
        ; Effect 7 = set waveform (inlined)
        ldy     chn_effectpar,x
        lda     wave_remap,y
        sta     chn_waveform,x
        jmp     @noeffect
@fx_1_to_3:
        ; Effects 1-3 (most common: arp, porta, speed)
        cmp     #2
        bcs     @fx_2_3
        ; Effect 1 = arpeggio (inlined)
        ldy     mod3counter
        beq     @arp_val2
        dey
        bne     @arp_val0
        lda     chn_effectpar,x
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        clc
        adc     chn_note,x
        bcc     @arp_done
@arp_val0:
        lda     chn_note,x
        bcc     @arp_done
@arp_val2:
        lda     chn_effectpar,x
        and     #$0f
        adc     chn_note,x
@arp_done:
        adc     chn_transpose,x
        jsr     set_notefreq_and_freqlo
        jmp     @noeffect
@fx_2_3:
        beq     @fx_2
        ; Effect 3 = speed (inlined)
        lda     speedcounter
        bne     @fx_exit1
        lda     chn_effectpar,x
        sta     speed
@fx_exit1:
        jmp     @noeffect
@fx_2:
        ; Effect 2 = tone portamento (inlined)
        lda     chn_effectpar,x
        tay
        asl     a
        asl     a
        asl     a
        asl     a
        sta     zp_ptr
        tya
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        sta     zp_ptr+1
        jsr     freqcmp
        bcc     @porta_up
        lda     chn_freqlo,x
        sbc     zp_ptr
        sta     chn_freqlo,x
        lda     chn_freqhi,x
        sbc     zp_ptr+1
        sta     chn_freqhi,x
        jsr     freqcmp
        bcs     @fx_exit1
        bcc     @porta_snap
@porta_up:
        lda     chn_freqlo,x
        adc     zp_ptr
        sta     chn_freqlo,x
        lda     chn_freqhi,x
        adc     zp_ptr+1
        sta     chn_freqhi,x
        jsr     freqcmp
        bcc     @fx_exit1
@porta_snap:
        lda     chn_notefreqlo,x
        sta     chn_freqlo,x
        lda     chn_notefreqhi,x
        sta     chn_freqhi,x
        jmp     @noeffect
@fx_8_to_e:
        cmp     #$c
        bcs     @fx_c_to_e
        cmp     #$a
        bcs     @fx_a_b
        cmp     #9
        bcs     @fx_9
        ; Effect 8 = pulse width (inlined)
        lda     chn_effectpar,x
        beq     @pulse_set
        lda     #$80
@pulse_set:
        jsr     set_pulse_width
        jmp     @noeffect
@fx_9:
        ; Effect 9 = set AD (inlined)
        ldy     chn_effectpar,x
        lda     ad_remap,y
        sta     chn_ad,x
        jmp     @noeffect
@fx_a_b:
        cmp     #$b
        bcs     @fx_b
        ; Effect A = filter resonance (inlined)
        ldy     chn_effectpar,x
        lda     reso_remap,y
        sta     filter_resonance
        jmp     @noeffect
@fx_b:
        ; Effect B = slide (inlined)
        lda     #$80
        sta     chn_slideenable,x
        lda     #$20
        sta     zp_ptr
        lda     #$00
        sta     zp_ptr+1
        ldy     chn_effectpar,x
        beq     @slide_up
        lda     chn_slidedelta_lo,x
        sec
        sbc     zp_ptr
        sta     chn_slidedelta_lo,x
        lda     chn_slidedelta_hi,x
        sbc     zp_ptr+1
        sta     chn_slidedelta_hi,x
        jmp     @noeffect
@slide_up:
        lda     chn_slidedelta_lo,x
        clc
        adc     zp_ptr
        sta     chn_slidedelta_lo,x
        lda     chn_slidedelta_hi,x
        adc     zp_ptr+1
        sta     chn_slidedelta_hi,x
        jmp     @noeffect
@fx_c_to_e:
        cmp     #$e
        bcs     @fx_e
        cmp     #$d
        bcs     @fx_d
        ; Effect C = global volume (inlined)
        lda     speedcounter
        bne     @fx_exit3
        lda     chn_effectpar,x
        sta     globalvolume
@fx_exit3:
        jmp     @noeffect
@fx_d:
        ; Effect D = filter mode (inlined)
        lda     speedcounter
        bne     @fx_exit3
        lda     chn_effectpar,x
        asl     a
        asl     a
        asl     a
        asl     a
        sta     filter_mode
        jmp     @noeffect
@fx_e:
        ; Effect E = position jump (inlined)
        lda     chn_effectpar,x
        sta     nextordernumber
        lda     #$80
        sta     forcenewpattern
@noeffect:
        lda     chn_slideenable,x
        beq     @noslide
        lda     chn_freqlo,x
        clc
        adc     chn_slidedelta_lo,x
        sta     chn_freqlo,x
        lda     chn_freqhi,x
        adc     chn_slidedelta_hi,x
        sta     chn_freqhi,x
@noslide:
        lda     chn_vibdepth,x
        beq     @novib
        jsr     calcvibrato
        lda     chn_vibpos,x
        clc
        adc     chn_vibspeed,x
        sta     chn_vibpos,x
        jmp     @nextchn
@novib:
        lda     chn_freqlo,x
        sta     chn_finfreqlo,x
        lda     chn_freqhi,x
        sta     chn_finfreqhi,x
@nextchn:
        inx
        cpx     #$03
        beq     @chnloop_done
        jmp     @chnloop
@chnloop_done:

        ldy     filter_idx
        beq     @filtdone
        jsr     get_filtertable_y
        sta     filter_cutoff
        iny
        cpy     filter_end
        bcc     @filtok
        beq     @filtok
        ldy     filter_loop
@filtok:
        sty     filter_idx
@filtdone:

        ldy     ordernumber
        lda     forcenewpattern
        bmi     @hr_newpat
        ldx     trackrow
        inx
        txa
        and     #$3f
        bne     @hr_setpos
@hr_newpat:
        ldx     #0
        ldy     nextordernumber
@hr_setpos:
        stx     hrtrackrow
        sty     hrordernumber
        lda     hrtrackrow
        sta     decode_row              ; set decode_row for HR get_trackptr_order calls
        and     #$0f            ; row_in_quarter = hrtrackrow & 15
        sta     trackrow3       ; temp storage
        asl     a               ; A = row_in_quarter * 2
        adc     trackrow3       ; A = row_in_quarter * 3
        sta     trackrow3
        lda     speedcounter
        adc     chn_hardrestart+0
        cmp     speed
        bcc     @hr1
        ldx     hrordernumber
        ldy     #0
        jsr     get_trackptr_order
        ldx     #0
        jsr     check_hardrestart
@hr1:
        lda     speedcounter
        clc
        adc     chn_hardrestart+1
        cmp     speed
        bcc     @hr2
        ldx     hrordernumber
        ldy     #1
        jsr     get_trackptr_order
        ldx     #1
        jsr     check_hardrestart
@hr2:
        lda     speedcounter
        clc
        adc     chn_hardrestart+2
        cmp     speed
        bcc     @dump
        ldx     hrordernumber
        ldy     #2
        jsr     get_trackptr_order
        ldx     #2
        jsr     check_hardrestart

@dump:
        ldx     #$00
@dumploop:
        ldy     sidregoffs,x
        lda     chn_plswidthlo,x
        sta     $d402,y
        lda     chn_plswidthhi,x
        sta     $d403,y
        lda     chn_finfreqlo,x
        sta     $d400,y
        lda     chn_finfreqhi,x
        sta     $d401,y
        lda     chn_waveform,x
        and     chn_gateon,x
        sta     $d404,y
        lda     chn_ad,x
        sta     $d405,y
        lda     chn_sr,x
        sta     $d406,y
        inx
        cpx     #$03
        bne     @dumploop
        lda     filter_cutoff
        sta     $d416
        lda     filter_resonance
        sta     $d417
        lda     globalvolume
        ora     filter_mode
        sta     $d418
        rts

; ============================================================================
; check_hardrestart - Check if hard restart needed for channel X
; ============================================================================
check_hardrestart:
        ldy     trackrow3
        lda     (zp_trackptr),y
        and     #$7f
        beq     @done
        cmp     #$61
        beq     @done
        lda     (zp_trackptr),y
        bmi     @do_hr
        iny
        lda     (zp_trackptr),y
        dey
        and     #$e0
        cmp     #$40              ; tone portamento (new effect 2 = bits 0-2 = 2 = 0x40 >> 5)
        beq     @done
@do_hr:
        lda     #$00
        sta     chn_waveform,x
        sta     chn_ad,x
        sta     chn_sr,x
@done:
        rts

; ============================================================================
; process_instrument - Wavetable, arpeggio, vibrato, pulse
; ============================================================================
process_instrument:
        ; Load cached inst pointer
        lda     chn_inst_ptr_lo,x
        sta     zp_ptr
        lda     chn_inst_ptr_hi,x
        sta     zp_ptr+1

        ldy     chn_waveidx,x
        lda     global_wavetable,y      ; Global wavetable (shared across all songs)
        sta     chn_waveform,x
        iny
        tya
        pha                             ; Save new waveidx
        ldy     #INST_WAVEEND
        lda     (zp_ptr),y              ; Inline inst access
        sta     pi_waveend+1
        pla                             ; Restore new waveidx
pi_waveend:
        cmp     #$00
        bcc     pi_waveset
        beq     pi_waveset
        ldy     #INST_WAVELOOP
        lda     (zp_ptr),y              ; Inline inst access
pi_waveset:
        sta     chn_waveidx,x

        lda     chn_effect,x
        cmp     #$02              ; tone portamento (old effect 3 -> new effect 2)
        beq     pi_skiparp
        ldy     chn_arpidx,x
arp_load_addr = *+1
        lda     $FFFF,y                 ; SMC: arptable address patched in init
        bmi     pi_arpabs
        clc
        adc     chn_note,x
        adc     chn_transpose,x
pi_arpabs:
        and     #$7f
        pha                             ; Save note value on stack
        ldy     chn_arpidx,x
        iny
        tya
        pha                             ; Save new arpidx
        ldy     #INST_ARPEND
        lda     (zp_ptr),y              ; Inline inst access
        sta     pi_arpend+1
        pla                             ; Restore new arpidx
pi_arpend:
        cmp     #$00
        bcc     pi_arpset
        beq     pi_arpset
        ldy     #INST_ARPLOOP
        lda     (zp_ptr),y              ; Inline inst access
pi_arpset:
        sta     chn_arpidx,x
        pla                             ; Restore note value
        tay
        lda     freqtable_lo,y
        sta     chn_notefreqlo,x
        sta     chn_freqlo,x
        lda     freqtable_hi,y
        sta     chn_notefreqhi,x
        sta     chn_freqhi,x
pi_skiparp:

        lda     chn_vibdelay,x
        bne     pi_vibdelayed
        ldy     #INST_VIBDEPSP
        lda     (zp_ptr),y              ; Inline inst access
        tay
        and     #$f0
        sta     chn_vibdepth,x
        beq     pi_pulse
        tya
        and     #$0f
        sta     chn_vibspeed,x
        jmp     pi_pulse
pi_vibdelayed:
        dec     chn_vibdelay,x
pi_pulse:
        lda     chn_plsspeed,x
        beq     pi_pulsedone
        lda     chn_plsdir,x
        beq     pi_pulseup
        lda     chn_plswidthlo,x
        sec
        sbc     chn_plsspeed,x
        sta     chn_plswidthlo,x
        lda     chn_plswidthhi,x
        sbc     #$00
        cmp     chn_plslimitdown,x
        bpl     pi_pulsestore
        lda     #$00
        sta     chn_plsdir,x
        sta     chn_plswidthlo,x
        lda     chn_plslimitdown,x
pi_pulsestore:
        sta     chn_plswidthhi,x
pi_pulsedone:
        rts
pi_pulseup:
        lda     chn_plswidthlo,x
        clc
        adc     chn_plsspeed,x
        sta     chn_plswidthlo,x
        lda     chn_plswidthhi,x
        adc     #$00
        cmp     chn_plslimitup,x
        bmi     pi_pulsestore
        beq     pi_pulsestore
        lda     #$80
        sta     chn_plsdir,x
        lda     #$ff
        sta     chn_plswidthlo,x
        lda     chn_plslimitup,x
        jmp     pi_pulsestore

; ============================================================================
; set_notefreq_and_freqlo - Set notefreq AND freqlo from note A
; ============================================================================
set_notefreq_and_freqlo:
        tay
        lda     freqtable_lo,y
        sta     chn_notefreqlo,x
        sta     chn_freqlo,x
        lda     freqtable_hi,y
        sta     chn_notefreqhi,x
        sta     chn_freqhi,x
        rts

; ============================================================================
; set_notefreq_only - Set only notefreq from note A
; ============================================================================
set_notefreq_only:
        tay
        lda     freqtable_lo,y
        sta     chn_notefreqlo,x
        lda     freqtable_hi,y
        sta     chn_notefreqhi,x
        rts

; ============================================================================
; calcvibrato
; ============================================================================
calcvibrato:
        lda     chn_vibpos,x
        and     #$1f
        cmp     #$10
        bcc     @haveval
        eor     #$1f
@haveval:
        ora     chn_vibdepth,x
        tay
        lda     vibrato_table-16,y      ; -16 because depth 0 row is deleted (never used)
        asl     a
        sta     zp_ptr
        lda     #$00
        rol     a
        sta     zp_ptr+1
        lda     chn_vibpos,x
        and     #$20
        bne     @add
        lda     chn_freqlo,x
        sec
        sbc     zp_ptr
        sta     chn_finfreqlo,x
        lda     chn_freqhi,x
        sbc     zp_ptr+1
        sta     chn_finfreqhi,x
        rts
@add:
        lda     chn_freqlo,x
        adc     zp_ptr
        sta     chn_finfreqlo,x
        lda     chn_freqhi,x
        adc     zp_ptr+1
        sta     chn_finfreqhi,x
        rts

; ============================================================================
; fetch_channel_row - Fetch row data for channel X
; ============================================================================
fetch_channel_row:
        ldy     trackrow3
        iny
        lda     (zp_trackptr),y
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        sta     chn_effect,x
        iny
        lda     (zp_trackptr),y
        sta     chn_effectpar,x
        dey
        dey
        lda     (zp_trackptr),y
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        and     #$08
        ora     chn_effect,x
        sta     chn_effect,x
        iny
        lda     (zp_trackptr),y
        and     #$1f
        beq     @checknote
        sta     chn_inst,x
        ; Setup zp_ptr = song_base + inst*16 (interleaved layout)
        asl
        asl
        asl
        asl                             ; A = inst*16 low byte
        sta     zp_ptr
        sta     chn_inst_ptr_lo,x       ; Cache low byte
        lda     #0
        rol                             ; Get carry from shift
        adc     zp_song_base+1
        sta     zp_ptr+1
        sta     chn_inst_ptr_hi,x       ; Cache high byte
        ; Load all instrument params with inline (zp_ptr),y
        ldy     #INST_AD
        lda     (zp_ptr),y
        sta     chn_ad,x
        ldy     #INST_SR
        lda     (zp_ptr),y
        sta     chn_sr,x
        ldy     #INST_WAVESTART
        lda     (zp_ptr),y
        sta     chn_waveidx,x
        ldy     #INST_ARPSTART
        lda     (zp_ptr),y
        sta     chn_arpidx,x
        ldy     #INST_VIBDELAY
        lda     (zp_ptr),y
        sta     chn_vibdelay,x
        ldy     #INST_PULSEWIDTH
        lda     (zp_ptr),y
        pha
        asl     a
        asl     a
        asl     a
        asl     a
        sta     chn_plswidthlo,x
        pla
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        sta     chn_plswidthhi,x
        ldy     #INST_PULSESPEED
        lda     (zp_ptr),y
        sta     chn_plsspeed,x
        ldy     #INST_PULSELIMITS
        lda     (zp_ptr),y
        pha
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        sta     chn_plslimitdown,x
        pla
        and     #$0f
        sta     chn_plslimitup,x
        lda     #$00
        sta     chn_plsdir,x
        sta     chn_vibpos,x
@checknote:
        ldy     trackrow3
        lda     (zp_trackptr),y
        and     #$7f
        beq     @done
        cmp     #$61
        beq     @noteoff
        sec
        sbc     #$01
        sta     chn_note,x
        ldy     chn_effect,x
        cpy     #$02              ; tone portamento (old effect 3 -> new effect 2)
        bne     @notporta
        clc
        adc     chn_transpose,x
        jsr     set_notefreq_only
@notporta:
        ; Load cached inst pointer
        lda     chn_inst_ptr_lo,x
        sta     zp_ptr
        lda     chn_inst_ptr_hi,x
        sta     zp_ptr+1
        ldy     #INST_WAVESTART
        lda     (zp_ptr),y
        sta     chn_waveidx,x
        ldy     #INST_ARPSTART
        lda     (zp_ptr),y
        sta     chn_arpidx,x
        lda     #$00
        sta     chn_slidedelta_lo,x
        sta     chn_slidedelta_hi,x
        sta     chn_slideenable,x
        lda     #$ff
        bne     @setgate
@noteoff:
        lda     #$fe
@setgate:
        sta     chn_gateon,x
@done:
        rts

; ============================================================================
; set_pulse_width - A = packed pulse width
; ============================================================================
set_pulse_width:
        pha
        asl     a
        asl     a
        asl     a
        asl     a
        sta     chn_plswidthlo,x
        pla
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        sta     chn_plswidthhi,x
        rts

freqcmp:
        lda     chn_freqhi,x
        cmp     chn_notefreqhi,x
        bne     @done
        lda     chn_freqlo,x
        cmp     chn_notefreqlo,x
@done:
        rts

; ============================================================================
; Data access routines for new format
; ============================================================================

; get_transpose_order - Get transpose for channel Y at order X
; Returns: A = transpose value
; Transpose tables at page-aligned offsets: ch0=$200, ch1=$300, ch2=$400
get_transpose_order:
        lda     transpose_page,y
        sta     zp_ptr+1
        lda     #0
        sta     zp_ptr
        txa
        tay
        lda     (zp_ptr),y
        rts

; get_trackptr_order - Set zp_trackptr for channel Y at order X
; Trackptr tables at page-aligned offsets: ch0=$500, ch1=$600, ch2=$700
; Uses streaming row-by-row decode: decodes single row into buffer
; Sets trackrow3 = 0 (row always at offset 0 in buffer)
get_trackptr_order:
        ; Save channel
        sty     @channel+1          ; SMC: save channel
        ; Get pattern index from page-aligned table
        lda     trackptr_page,y
        sta     zp_ptr+1
        lda     #0
        sta     zp_ptr
        txa
        tay
        lda     (zp_ptr),y          ; A = pattern index
        sta     @pat_idx
@channel:
        ldx     #0                  ; SMC: channel
        ; Check if this exact row is already decoded
        cmp     chn_decoded_pat,x
        bne     @need_decode
        lda     decode_row
        cmp     chn_decoded_row,x
        beq     @already_decoded
        ; Same pattern, different row - check if sequential
        lda     chn_decoded_row,x
        clc
        adc     #1
        cmp     decode_row
        beq     @advance_one        ; sequential, decode next row
@need_decode:
        ; Different pattern or non-sequential - restart from row 0
        lda     @pat_idx
        sta     chn_decoded_pat,x
        ; Reset decoder state
        lda     #0
        sta     chn_src_off,x
        sta     chn_rle_count,x
        sta     chn_prev_row_0,x
        sta     chn_prev_row_1,x
        sta     chn_prev_row_2,x
        lda     #$ff
        sta     chn_decoded_row,x   ; mark as "before row 0"
        ; Skip to target row
@skip_loop:
        lda     chn_decoded_row,x
        cmp     decode_row
        beq     @store_to_buffer    ; reached target, decode and store
        jsr     decode_advance_row  ; decode (skip) one row
        inc     chn_decoded_row,x
        jmp     @skip_loop
@advance_one:
        ; Sequential advance - decode one more row
        jsr     decode_advance_row
@store_to_buffer:
        ; Copy current row (prev_row) to decode buffer
        lda     decode_buffer_lo,x
        sta     zp_decode_dst
        lda     decode_buffer_hi,x
        sta     zp_decode_dst+1
        ldy     #0
        lda     chn_prev_row_0,x
        sta     (zp_decode_dst),y
        iny
        lda     chn_prev_row_1,x
        sta     (zp_decode_dst),y
        iny
        lda     chn_prev_row_2,x
        sta     (zp_decode_dst),y
        ; Update decoded row
        lda     decode_row
        sta     chn_decoded_row,x
@already_decoded:
        ; Set zp_trackptr to channel's decode buffer
        lda     decode_buffer_lo,x
        sta     zp_trackptr
        lda     decode_buffer_hi,x
        sta     zp_trackptr+1
        ; Row is always at offset 0
        lda     #0
        sta     trackrow3
        rts
@pat_idx:       .byte   0

; Decode buffer address table
decode_buffer_lo:
        .byte   <decode_buffer_0, <decode_buffer_1, <decode_buffer_2
decode_buffer_hi:
        .byte   >decode_buffer_0, >decode_buffer_1, >decode_buffer_2

; decode_advance_row - Decode one row, updating prev_row and state
; Input: X = channel (chn_decoded_pat,x must contain pattern index)
; Updates: chn_src_off, chn_rle_count, chn_prev_row_*
decode_advance_row:
        ; Check for pending RLE
        lda     chn_rle_count,x
        beq     @not_rle
        ; RLE: prev_row stays same, just decrement count
        dec     chn_rle_count,x
        rts
@not_rle:
        ; Set up source pointer from pattern index
        ; Calculate source: packed_data + packed_ptrs[pattern_idx]
        lda     chn_decoded_pat,x
        asl     a                   ; pattern index * 2
        tay
        lda     packed_ptrs_addr
        sta     zp_ptr
        lda     packed_ptrs_addr+1
        sta     zp_ptr+1
        lda     (zp_ptr),y          ; ptr lo
        clc
        adc     zp_packed_data
        sta     zp_decode_src
        iny
        lda     (zp_ptr),y          ; ptr hi
        adc     zp_packed_data+1
        sta     zp_decode_src+1
        ; Add current source offset
        lda     chn_src_off,x
        clc
        adc     zp_decode_src
        sta     zp_decode_src
        lda     #0
        adc     zp_decode_src+1
        sta     zp_decode_src+1
        ; Read encoded byte
        ldy     #0
        lda     (zp_decode_src),y
        cmp     #$E0
        bcs     @check_rle
        ; 0x00-0xDF: primary dict index
        jmp     @copy_dict_entry
@check_rle:
        cmp     #$FF
        beq     @extended
        ; 0xE0-0xFE: RLE 1-31 (repeat prev row)
        sec
        sbc     #$DF                ; count = byte - 0xDF (1-31)
        sta     chn_rle_count,x
        inc     chn_src_off,x       ; advance past RLE byte
        dec     chn_rle_count,x     ; consume one repeat for this row
        rts                         ; prev_row unchanged
@extended:
        ; 0xFF + byte: extended dict index = 224 + next byte
        inc     chn_src_off,x       ; skip 0xFF marker
        ldy     #1
        lda     (zp_decode_src),y   ; get extended byte
        clc
        adc     #224                ; index lo = 224 + byte
        sta     @dict_idx
        lda     #0
        adc     #0                  ; index hi = carry
        sta     @dict_idx_hi
        jmp     @copy_dict_entry_16

; Copy dict entry A to prev_row (primary index < 224)
@copy_dict_entry:
        sta     @dict_idx
        lda     #0
        sta     @dict_idx_hi
        ; Fall through to 16-bit version

; Copy dict entry to chn_prev_row_* for channel X
@copy_dict_entry_16:
        ; Calculate dict offset: index * 3 (16-bit index)
        ; index*3 = index + index*2
        lda     @dict_idx
        asl     a                   ; A*2 lo
        sta     zp_ptr
        lda     @dict_idx_hi
        rol     a                   ; A*2 hi
        sta     zp_ptr+1
        ; Add original index
        lda     zp_ptr
        clc
        adc     @dict_idx
        sta     zp_ptr
        lda     zp_ptr+1
        adc     @dict_idx_hi
        sta     zp_ptr+1
        ; Add row_dict base
        lda     zp_ptr
        clc
        adc     zp_row_dict
        sta     zp_ptr
        lda     zp_ptr+1
        adc     zp_row_dict+1
        sta     zp_ptr+1
        ; Copy 3 bytes to prev_row
        ldy     #0
        lda     (zp_ptr),y
        sta     chn_prev_row_0,x
        iny
        lda     (zp_ptr),y
        sta     chn_prev_row_1,x
        iny
        lda     (zp_ptr),y
        sta     chn_prev_row_2,x
        inc     chn_src_off,x       ; advance past dict index byte
        rts

; Local variables for decode
@dict_idx:      .byte   0
@dict_idx_hi:   .byte   0

; get_filtertable_y - Get filtertable entry Y (SMC: address patched in init)
get_filtertable_y:
filter_load:
        lda     $FFFF,y
        rts

; ============================================================================
; Tables
; ============================================================================

; Effect dispatch uses binary search instead of jump table (see @haseffect)
; Effect 0: param 0=nop, 1=vib, 2=break, 3=fineslide
; Effects 1-E: arp, porta, speed, hrdrest, filttrig, SR, wave, pulse, AD, reso, slide, globalvol, filtmode, jump

sidregoffs:
        .byte   0, 7, 14

; Vibrato table (9 rows, depth 0 deleted - code skips lookup when depth=0)
; Frequency order: 4(22) 2(13) 3(11) 1(6) 6(2) 10(1) 5(1) 8(1) 15(1)
vibrato_table:
        .byte   $00,$06,$0c,$13,$18,$1e,$24,$29,$2d,$31,$35,$38,$3b,$3d,$3f,$40  ; old depth 4 -> new 1
        .byte   $00,$03,$06,$09,$0c,$0f,$12,$14,$17,$19,$1b,$1c,$1e,$1f,$1f,$20  ; old depth 2 -> new 2
        .byte   $00,$05,$09,$0e,$12,$17,$1b,$1e,$22,$25,$28,$2a,$2c,$2e,$2f,$30  ; old depth 3 -> new 3
        .byte   $00,$02,$03,$05,$06,$08,$09,$0a,$0b,$0c,$0d,$0e,$0f,$0f,$10,$10  ; old depth 1 -> new 4
        .byte   $00,$09,$13,$1c,$25,$2d,$35,$3d,$44,$4a,$50,$55,$59,$5c,$5e,$60  ; old depth 6 -> new 5
        .byte   $00,$00,$00,$00,$00,$00,$00,$66,$71,$00,$00,$00,$00,$00,$00,$9f  ; old depth 10 -> new 6 (cols 1-6,9-14 unused)
        .byte   $00,$08,$10,$17,$1f,$26,$2c,$33,$39,$3e,$43,$47,$4a,$4d,$4e,$50  ; old depth 5 -> new 7
        .byte   $00,$0d,$19,$25,$31,$3c,$47,$51,$5b,$63,$6a,$71,$76,$7a,$7e,$7f  ; old depth 8 -> new 8
        .byte   $00,$18,$2f,$46,$5c,$71,$85,$98,$aa,$ba,$c8,$d4,$de,$e6,$eb,$ef  ; old depth 15 -> new 9

; Effect parameter remap tables (frequency-sorted)
ad_remap:                               ; 7(AD): $08(96), $09(53), $48(38), $0A(32)
        .byte   $08, $09, $48, $0A
sr_remap:                               ; 8(SR): $F9(128), $0D(123), $FF(118), $F8(48), $0F(16), $0E(13)
        .byte   $F9, $0D, $FF, $F8, $0F, $0E
wave_remap:                             ; 9(wave): $FF(175), $80(90), $43(64), $81(58)
        .byte   $FF, $80, $43, $81
reso_remap:                             ; E(reso): $F1(50), $00(34), $F4(12), $F0(8), $F2(2), $52(2), $F5(1)
        .byte   $F1, $00, $F4, $F0, $F2, $52, $F5

; ============================================================================
; Player Variables
; ============================================================================

chn_hardrestart:        .byte   0, 0, 0
chn_gateon:             .byte   0, 0, 0
chn_slideenable:        .byte   0, 0, 0
chn_plsdir:             .byte   0, 0, 0
chn_transpose:          .byte   0, 0, 0
chn_note:               .byte   0, 0, 0
chn_inst:               .byte   0, 0, 0
chn_inst_ptr_lo:        .byte   0, 0, 0
chn_inst_ptr_hi:        .byte   0, 0, 0
chn_effect:             .byte   0, 0, 0
chn_effectpar:          .byte   0, 0, 0
chn_waveform:           .byte   0, 0, 0
chn_ad:                 .byte   0, 0, 0
chn_sr:                 .byte   0, 0, 0
chn_plswidthlo:         .byte   0, 0, 0
chn_plswidthhi:         .byte   0, 0, 0
chn_plsspeed:           .byte   0, 0, 0
chn_plslimitdown:       .byte   0, 0, 0
chn_plslimitup:         .byte   0, 0, 0
chn_vibdelay:           .byte   0, 0, 0
chn_vibdepth:           .byte   0, 0, 0
chn_vibspeed:           .byte   0, 0, 0
chn_waveidx:            .byte   0, 0, 0
chn_arpidx:             .byte   0, 0, 0
chn_notefreqlo:         .byte   0, 0, 0
chn_notefreqhi:         .byte   0, 0, 0
chn_freqlo:             .byte   0, 0, 0
chn_freqhi:             .byte   0, 0, 0
chn_finfreqlo:          .byte   0, 0, 0
chn_finfreqhi:          .byte   0, 0, 0
chn_vibpos:             .byte   0, 0, 0
chn_slidedelta_lo:      .byte   0, 0, 0
chn_slidedelta_hi:      .byte   0, 0, 0
filter_idx:             .byte   0
filter_end:             .byte   0
filter_loop:            .byte   0
filter_cutoff:          .byte   0
filter_resonance:       .byte   0
filter_mode:            .byte   0
globalvolume:           .byte   0
mod3counter:            .byte   0
speed:                  .byte   0
speedcounter:           .byte   0
trackrow:               .byte   0
trackrow3:              .byte   0
hrtrackrow:             .byte   0
decode_row:             .byte   0       ; current row for pattern decode
ordernumber:            .byte   0
nextordernumber:        .byte   0
hrordernumber:          .byte   0
forcenewpattern:        .byte   0

PLAYERVARSSIZE = * - chn_hardrestart

; Values computed from song base during init
transpose_page:         .byte   0, 0, 0
trackptr_page:          .byte   0, 0, 0
packed_ptrs_addr:       .word   0       ; Address of packed pointers table

; Pattern decode state (row-by-row streaming)
chn_decoded_pat:        .byte   $FF, $FF, $FF  ; Pattern index in each channel's decode buffer
chn_decoded_row:        .byte   $FF, $FF, $FF  ; Which row (0-63) is decoded

; Decoder state per channel for streaming decode
chn_src_off:            .byte   0, 0, 0        ; Byte offset into packed pattern data
chn_rle_count:          .byte   0, 0, 0        ; RLE count remaining
chn_prev_row_0:         .byte   0, 0, 0        ; prev_row byte 0 (note) per channel
chn_prev_row_1:         .byte   0, 0, 0        ; prev_row byte 1 (inst|eff) per channel
chn_prev_row_2:         .byte   0, 0, 0        ; prev_row byte 2 (param) per channel

; Decode buffers (3 bytes × 3 channels = 9 bytes) - single row per channel
decode_buffer_0:        .res    3
decode_buffer_1:        .res    3
decode_buffer_2:        .res    3

; Split frequency tables (104 entries: notes 0-103)
; Note 103 holds remapped absolute note value (was 127)
freqtable_lo:
        .byte   $12,$00,$00,$46,$5a,$6e,$84,$9b,$b3,$cd,$e9,$06
        .byte   $25,$45,$68,$8c,$b3,$dc,$08,$36,$67,$9b,$d2,$0c
        .byte   $49,$8b,$d0,$19,$67,$b9,$10,$6c,$ce,$35,$a3,$17
        .byte   $93,$15,$9f,$32,$cd,$72,$20,$d8,$9c,$6b,$46,$2f
        .byte   $25,$2a,$3f,$64,$9a,$e3,$3f,$b1,$38,$d6,$8d,$5e
        .byte   $4b,$55,$7e,$c8,$34,$c6,$7f,$61,$6f,$ac,$1a,$bc
        .byte   $95,$a9,$fc,$8f,$69,$8c,$fe,$c2,$df,$58,$34,$78
        .byte   $2b,$53,$f7,$1f,$d2,$19,$fc,$85,$bd,$b0,$67,$ff
        ; Notes 96-103: 96-102 unused (read $00), 103 = remapped from 127
        .byte   $00,$00,$00,$00,$00,$00,$00,$2f
freqtable_hi:
        .byte   $01,$00,$00,$01,$01,$01,$01,$01,$01,$01,$01,$02
        .byte   $02,$02,$02,$02,$02,$02,$03,$03,$03,$03,$03,$04
        .byte   $04,$04,$04,$05,$05,$05,$06,$06,$06,$07,$07,$08
        .byte   $08,$09,$09,$0a,$0a,$0b,$0c,$0c,$0d,$0e,$0f,$10
        .byte   $11,$12,$13,$14,$15,$16,$18,$19,$1b,$1c,$1e,$20
        .byte   $22,$24,$26,$28,$2b,$2d,$30,$33,$36,$39,$3d,$40
        .byte   $44,$48,$4c,$51,$56,$5b,$60,$66,$6c,$73,$7a,$81
        .byte   $89,$91,$99,$a3,$ac,$b7,$c1,$cd,$d9,$e6,$f4,$ff
        ; Notes 96-103: 96-102 unused (read $00), 103 = remapped from 127
        .byte   $00,$00,$00,$00,$00,$00,$00,$30

; Global wavetable (shared across all songs)
.include "../generated/wavetable.inc"
