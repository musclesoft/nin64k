.PHONY: all clean

BUILD = ../../build

all: $(BUILD)/player.bin odin_convert

$(BUILD)/player.bin: player_standalone.asm ../../src/odin_player.inc
	@mkdir -p $(BUILD)
	ca65 -o $(BUILD)/player.o player_standalone.asm
	ld65 -C player.cfg -o $(BUILD)/player.bin $(BUILD)/player.o

odin_convert: main.go
	go build -o odin_convert main.go

clean:
	rm -f $(BUILD)/player.o $(BUILD)/player.bin odin_convert *.lst
