; ============================================================================
; Stream data - loaded directly at $6000
; ============================================================================

.segment "STREAM"

stream_start:
.ifdef USE_STREAM2
        .incbin "../generated/stream2.bin"
.else
        .incbin "../generated/stream.bin"
.endif
stream_end:

STREAM_START = stream_start
STREAM_SIZE = stream_end - stream_start
