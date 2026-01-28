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

; Zero page usage (contiguous block $70-$75)
zp_song_base     = $70          ; 2 bytes: base address of current song data
zp_ptr           = $72          ; 2 bytes: temp pointer / temp values
zp_trackptr      = $74          ; 2 bytes: track pointer

; New format: packed offsets (all tables deduplicated)
; $000: Instruments (512 bytes) - interleaved: inst N at offset N*16
; $200: Transpose ch0-2 (256 bytes each)
; $500: Trackptr ch0-2 (256 bytes each)
; $800: Filtertable (234 bytes max)
; $8EA: Wavetable (51 bytes max)
; $91D: Arptable (188 bytes max)
; $9D9: Patterns

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
        stx     wave_load_addr+1        ; $8EA high byte
        stx     filter_load+2           ; $800 high byte
        inx                             ; X = base + 9
        stx     arp_load_addr+1         ; $91D high byte
        lda     #$ea
        sta     wave_load_addr
        lda     #$1d
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
        lda     firsttrackrow
        sta     trackrow
        lda     #$00
        sta     firsttrackrow
        sta     forcenewpattern
@pattern_ok:
        lda     trackrow
        asl     a
        adc     trackrow
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
        beq     @noeffect
        asl     a
        tay
        lda     effectptrs-2,y
        sta     @effectjsr+1
        lda     effectptrs-1,y
        sta     @effectjsr+2
@effectjsr:
        jsr     $ffff
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
        bne     @chnloop

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
        ldx     firsttrackrow
        ldy     nextordernumber
@hr_setpos:
        stx     hrtrackrow
        sty     hrordernumber
        lda     hrtrackrow
        asl     a
        adc     hrtrackrow
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
        cmp     #$60
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
wave_load_addr = *+1
        lda     $FFFF,y                 ; SMC: wavetable address patched in init
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
        cmp     #$03
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
        lda     vibrato_table,y
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
        cpy     #$03
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

; ============================================================================
; Effects (remapped: 1,2,3,4,7,8,9,A,B,D,E,F -> 1-C)
; ============================================================================

effect01:
        lda     #$80
        sta     chn_slideenable,x
        lda     #$20
        sta     zp_ptr
        lda     #$00
        sta     zp_ptr+1
        ldy     chn_effectpar,x
        bmi     @up
        lda     chn_slidedelta_lo,x
        sec
        sbc     zp_ptr
        sta     chn_slidedelta_lo,x
        lda     chn_slidedelta_hi,x
        sbc     zp_ptr+1
        sta     chn_slidedelta_hi,x
        rts
@up:
        lda     chn_slidedelta_lo,x
        clc
        adc     zp_ptr
        sta     chn_slidedelta_lo,x
        lda     chn_slidedelta_hi,x
        adc     zp_ptr+1
        sta     chn_slidedelta_hi,x
        rts

effect02:
        lda     chn_effectpar,x
        jmp     set_pulse_width

effect03:
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
        bcc     @up
        lda     chn_freqlo,x
        sbc     zp_ptr
        sta     chn_freqlo,x
        lda     chn_freqhi,x
        sbc     zp_ptr+1
        sta     chn_freqhi,x
        jsr     freqcmp
        bcs     @done
        bcc     @snap
@up:
        lda     chn_freqlo,x
        adc     zp_ptr
        sta     chn_freqlo,x
        lda     chn_freqhi,x
        adc     zp_ptr+1
        sta     chn_freqhi,x
        jsr     freqcmp
        bcc     @done
@snap:
        lda     chn_notefreqlo,x
        sta     chn_freqlo,x
        lda     chn_notefreqhi,x
        sta     chn_freqhi,x
@done:
        rts

effect04:
        lda     chn_effectpar,x
        tay
        and     #$f0
        sta     chn_vibdepth,x
        tya
        and     #$0f
        sta     chn_vibspeed,x
        rts

effect07:
        lda     chn_effectpar,x
        sta     chn_ad,x
        rts

effect08:
        lda     chn_effectpar,x
        sta     chn_sr,x
        rts

effect09:
        lda     chn_effectpar,x
        sta     chn_waveform,x
        rts

effect0a:
        ldy     mod3counter
        beq     @val2
        dey
        bne     @val0
        lda     chn_effectpar,x
        lsr     a
        lsr     a
        lsr     a
        lsr     a
        clc
        adc     chn_note,x
        bcc     @done
@val0:
        lda     chn_note,x
        bcc     @done
@val2:
        lda     chn_effectpar,x
        and     #$0f
        adc     chn_note,x
@done:
        adc     chn_transpose,x
        jmp     set_notefreq_and_freqlo

effect0b:
        lda     chn_effectpar,x
        sta     nextordernumber
        lda     #$80
        sta     forcenewpattern
        rts

effect0d:
        lda     chn_effectpar,x
        sta     firsttrackrow
        lda     #$80
        sta     forcenewpattern
        rts

effect0e:
        lda     chn_effectpar,x
        sta     filter_resonance
        rts

effect0f:
        lda     speedcounter
        bne     @done
        lda     chn_effectpar,x
        bpl     @speed
        and     #$f0
        tay
        lda     chn_effectpar,x
        and     #$0f
        cpy     #$80
        beq     @f8x
        cpy     #$90
        beq     @f9x
        cpy     #$b0
        beq     @fbx
        cpy     #$e0
        beq     @fex
        cpy     #$f0
        bne     @done
        sta     chn_hardrestart,x
@done:
        rts
@speed:
        sta     speed
        rts
@f8x:
        sta     globalvolume
        rts
@f9x:
        asl     a
        asl     a
        asl     a
        asl     a
        sta     filter_mode
        rts
@fbx:
        asl     a
        asl     a
        adc     chn_slidedelta_lo,x
        sta     chn_slidedelta_lo,x
        lda     chn_slidedelta_hi,x
        adc     #$00
        sta     chn_slidedelta_hi,x
        lda     #$80
        sta     chn_slideenable,x
        rts
@fex:
        cmp     #0              ; Set Z flag from A (inst number)
        beq     @done           ; FE0 = no filter trigger
        ; Setup zp_ptr = song_base + inst*16 (interleaved layout)
        asl
        asl
        asl
        asl                     ; A = inst*16 low byte
        sta     zp_ptr
        lda     #0
        rol                     ; Get carry from shift
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
; Trackptr tables at page-aligned offsets: ch0=$600, ch1=$700, ch2=$800
get_trackptr_order:
        ; Get pattern index from page-aligned table
        lda     trackptr_page,y
        sta     zp_ptr+1
        lda     #0
        sta     zp_ptr
        txa
        tay
        lda     (zp_ptr),y          ; A = pattern index
        tay                         ; Y = pattern index for lookup
        ; Look up pre-computed pattern address from constant tables
        lda     pattern_off_lo,y
        clc
        adc     zp_song_base
        sta     zp_trackptr
        lda     pattern_off_hi,y
        adc     zp_song_base+1
        sta     zp_trackptr+1
        rts

; get_filtertable_y - Get filtertable entry Y (SMC: address patched in init)
get_filtertable_y:
filter_load:
        lda     $FFFF,y
        rts

; ============================================================================
; Tables
; ============================================================================

; Effect table - remapped for compactness (indexed from 1)
; 1-C map to old effects: 1,2,3,4,7,8,9,A,B,D,E,F
effectptrs:
        .word   effect01      ; 1 = old 1 (portamento up)
        .word   effect02      ; 2 = old 2 (portamento down)
        .word   effect03      ; 3 = old 3 (tone portamento)
        .word   effect04      ; 4 = old 4 (vibrato)
        .word   effect07      ; 5 = old 7 (set AD)
        .word   effect08      ; 6 = old 8 (set SR)
        .word   effect09      ; 7 = old 9 (set waveform)
        .word   effect0a      ; 8 = old A (arpeggio)
        .word   effect0b      ; 9 = old B (position jump)
        .word   effect0d      ; A = old D (pattern break)
        .word   effect0e      ; B = old E (set filter resonance)
        .word   effect0f      ; C = old F (extended effects)

sidregoffs:
        .byte   0, 7, 14

; Vibrato table (10 rows for remapped depths 0-9)
; Old depths 0,1,2,3,4,5,6,8,10,15 -> new depths 0,1,2,3,4,5,6,7,8,9
vibrato_table:
        .byte   $00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00,$00  ; depth 0
        .byte   $00,$02,$03,$05,$06,$08,$09,$0a,$0b,$0c,$0d,$0e,$0f,$0f,$10,$10  ; depth 1
        .byte   $00,$03,$06,$09,$0c,$0f,$12,$14,$17,$19,$1b,$1c,$1e,$1f,$1f,$20  ; depth 2
        .byte   $00,$05,$09,$0e,$12,$17,$1b,$1e,$22,$25,$28,$2a,$2c,$2e,$2f,$30  ; depth 3
        .byte   $00,$06,$0c,$13,$18,$1e,$24,$29,$2d,$31,$35,$38,$3b,$3d,$3f,$40  ; depth 4
        .byte   $00,$08,$10,$17,$1f,$26,$2c,$33,$39,$3e,$43,$47,$4a,$4d,$4e,$50  ; depth 5
        .byte   $00,$09,$13,$1c,$25,$2d,$35,$3d,$44,$4a,$50,$55,$59,$5c,$5e,$60  ; depth 6
        .byte   $00,$0d,$19,$25,$31,$3c,$47,$51,$5b,$63,$6a,$71,$76,$7a,$7e,$7f  ; old depth 8 -> new 7
        .byte   $00,$00,$00,$00,$00,$00,$00,$66,$71,$00,$00,$00,$00,$00,$00,$9f  ; old depth 10 -> new 8 (cols 1-6,9-14 unused)
        .byte   $00,$18,$2f,$46,$5c,$71,$85,$98,$aa,$ba,$c8,$d4,$de,$e6,$eb,$ef  ; old depth 15 -> new 9

; Pattern offset lookup tables (offset = $9D9 + index * 192)
; 91 entries for patterns 0-90
pattern_off_lo:
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59,$19
        .byte   $d9,$99,$59,$19,$d9,$99,$59,$19,$d9,$99,$59

pattern_off_hi:
        .byte   $09,$0a,$0b,$0c,$0c,$0d,$0e,$0f,$0f,$10,$11,$12,$12,$13,$14,$15
        .byte   $15,$16,$17,$18,$18,$19,$1a,$1b,$1b,$1c,$1d,$1e,$1e,$1f,$20,$21
        .byte   $21,$22,$23,$24,$24,$25,$26,$27,$27,$28,$29,$2a,$2a,$2b,$2c,$2d
        .byte   $2d,$2e,$2f,$30,$30,$31,$32,$33,$33,$34,$35,$36,$36,$37,$38,$39
        .byte   $39,$3a,$3b,$3c,$3c,$3d,$3e,$3f,$3f,$40,$41,$42,$42,$43,$44,$45
        .byte   $45,$46,$47,$48,$48,$49,$4a,$4b,$4b,$4c,$4d

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
firsttrackrow:          .byte   0
ordernumber:            .byte   0
nextordernumber:        .byte   0
hrordernumber:          .byte   0
forcenewpattern:        .byte   0

PLAYERVARSSIZE = * - chn_hardrestart

; Values computed from song base during init
transpose_page:         .byte   0, 0, 0
trackptr_page:          .byte   0, 0, 0

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
