ASM = ca65
LD = ld65

SRC = src/nin64k.asm
CFG = src/c64.cfg
OBJ = build/nin64k.o
PRG = build/nin64k.prg

SELFTEST_SRC = src/nin64selftest.asm
SELFTEST_OBJ = build/nin64selftest.o
SELFTEST_PRG = build/nin64selftest.prg

SID_SRC = src/nin64sid.asm
SID_CFG = src/sid.cfg
SID_OBJ = build/nin64sid.o
SID_FILE = build/Nine_Inch_Ninjas.sid

INCLUDES = $(wildcard src/*.inc)

.PHONY: all clean run selftest run-selftest sid

all: $(PRG) $(SID_FILE)

$(OBJ): $(SRC) $(INCLUDES) generated/decompress.asm generated/stream_main.bin generated/stream_tail.bin
	@mkdir -p build
	$(ASM) -o $@ $<

$(PRG): $(OBJ) $(CFG)
	$(LD) -C $(CFG) -o $@ $<

run: $(PRG)
ifdef VICE_BIN
	$(VICE_BIN)/x64sc -autostartprgmode 1 +confirmonexit $(PRG) &
else
	@echo "Set VICE_BIN to run in emulator, e.g.: export VICE_BIN=~/path/to/vice/bin"
endif

selftest: $(SELFTEST_PRG)

$(SELFTEST_OBJ): $(SELFTEST_SRC) $(INCLUDES) generated/decompress.asm generated/stream_main.bin generated/stream_tail.bin
	@mkdir -p build
	$(ASM) -o $@ $<

$(SELFTEST_PRG): $(SELFTEST_OBJ) $(CFG)
	$(LD) -C $(CFG) -o $@ $<

run-selftest: $(SELFTEST_PRG)
ifdef VICE_BIN
	$(VICE_BIN)/x64sc -autostartprgmode 1 -warp +confirmonexit $(SELFTEST_PRG) &
else
	@echo "Set VICE_BIN to run in emulator, e.g.: export VICE_BIN=~/path/to/vice/bin"
endif

sid: $(SID_FILE)

$(SID_OBJ): $(SID_SRC) $(INCLUDES) generated/decompress.asm generated/part1.bin generated/stream_main.bin generated/stream_tail.bin
	@mkdir -p build
	$(ASM) -o $@ $<

$(SID_FILE): $(SID_OBJ) $(SID_CFG)
	$(LD) -C $(SID_CFG) -o $@ $<

clean:
	rm -rf build/*.o build/*.prg build/*.sid build/*.bin build/*.inc
