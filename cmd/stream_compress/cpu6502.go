package main

// Minimal 6502 CPU emulator for capturing SID frequency writes

type SIDWrite struct {
	Addr  uint16
	Value byte
	Frame int
}

type CPU6502 struct {
	A, X, Y      byte
	SP           byte
	PC           uint16
	P            byte
	Memory       [65536]byte
	SIDWrites    []SIDWrite
	CurrentFrame int
}

const (
	FlagC byte = 1 << 0
	FlagZ byte = 1 << 1
	FlagI byte = 1 << 2
	FlagD byte = 1 << 3
	FlagB byte = 1 << 4
	FlagU byte = 1 << 5
	FlagV byte = 1 << 6
	FlagN byte = 1 << 7
)

func NewCPU() *CPU6502 {
	return &CPU6502{SP: 0xFF, P: FlagU | FlagI}
}

func (c *CPU6502) Reset() {
	c.A, c.X, c.Y = 0, 0, 0
	c.SP = 0xFF
	c.P = FlagU | FlagI
	c.SIDWrites = nil
}

func (c *CPU6502) Read(addr uint16) byte {
	return c.Memory[addr]
}

func (c *CPU6502) Write(addr uint16, val byte) {
	if addr >= 0xD400 && addr <= 0xD418 {
		c.SIDWrites = append(c.SIDWrites, SIDWrite{Addr: addr, Value: val, Frame: c.CurrentFrame})
	}
	c.Memory[addr] = val
}

func (c *CPU6502) push(val byte) {
	c.Memory[0x100+uint16(c.SP)] = val
	c.SP--
}

func (c *CPU6502) pop() byte {
	c.SP++
	return c.Memory[0x100+uint16(c.SP)]
}

func (c *CPU6502) setNZ(val byte) {
	c.P &^= FlagN | FlagZ
	if val == 0 {
		c.P |= FlagZ
	}
	if val&0x80 != 0 {
		c.P |= FlagN
	}
}

func (c *CPU6502) branch(cond bool) {
	offset := int8(c.Read(c.PC))
	c.PC++
	if cond {
		c.PC = uint16(int32(c.PC) + int32(offset))
	}
}

func (c *CPU6502) Step() bool {
	op := c.Read(c.PC)
	c.PC++

	switch op {
	case 0x00: // BRK
		return false
	case 0x01: // ORA (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.A |= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x05: // ORA zp
		c.A |= c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.A)
	case 0x06: // ASL zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr)
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val <<= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x08: // PHP
		c.push(c.P | FlagB | FlagU)
	case 0x09: // ORA #imm
		c.A |= c.Read(c.PC)
		c.PC++
		c.setNZ(c.A)
	case 0x0A: // ASL A
		c.P &^= FlagC
		if c.A&0x80 != 0 {
			c.P |= FlagC
		}
		c.A <<= 1
		c.setNZ(c.A)
	case 0x0D: // ORA abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A |= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x0E: // ASL abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr)
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val <<= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x10: // BPL
		c.branch(c.P&FlagN == 0)
	case 0x11: // ORA (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.A |= c.Read(addr)
		c.setNZ(c.A)
	case 0x15: // ORA zp,X
		c.A |= c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF)
		c.PC++
		c.setNZ(c.A)
	case 0x16: // ASL zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr)
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val <<= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x18: // CLC
		c.P &^= FlagC
	case 0x19: // ORA abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A |= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y))
		c.setNZ(c.A)
	case 0x1D: // ORA abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A |= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X))
		c.setNZ(c.A)
	case 0x1E: // ASL abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr)
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val <<= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x20: // JSR
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		retAddr := c.PC - 1
		c.push(byte(retAddr >> 8))
		c.push(byte(retAddr))
		c.PC = uint16(lo) | uint16(hi)<<8
	case 0x21: // AND (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.A &= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x24: // BIT zp
		val := c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.P &^= FlagN | FlagV | FlagZ
		if val&0x80 != 0 {
			c.P |= FlagN
		}
		if val&0x40 != 0 {
			c.P |= FlagV
		}
		if c.A&val == 0 {
			c.P |= FlagZ
		}
	case 0x25: // AND zp
		c.A &= c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.A)
	case 0x26: // ROL zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val = val<<1 | carry
		c.Write(addr, val)
		c.setNZ(val)
	case 0x28: // PLP
		c.P = c.pop() | FlagU
	case 0x29: // AND #imm
		c.A &= c.Read(c.PC)
		c.PC++
		c.setNZ(c.A)
	case 0x2A: // ROL A
		carry := c.P & FlagC
		c.P &^= FlagC
		if c.A&0x80 != 0 {
			c.P |= FlagC
		}
		c.A = c.A<<1 | carry
		c.setNZ(c.A)
	case 0x2C: // BIT abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		val := c.Read(uint16(lo) | uint16(hi)<<8)
		c.P &^= FlagN | FlagV | FlagZ
		if val&0x80 != 0 {
			c.P |= FlagN
		}
		if val&0x40 != 0 {
			c.P |= FlagV
		}
		if c.A&val == 0 {
			c.P |= FlagZ
		}
	case 0x2D: // AND abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A &= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x2E: // ROL abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val = val<<1 | carry
		c.Write(addr, val)
		c.setNZ(val)
	case 0x30: // BMI
		c.branch(c.P&FlagN != 0)
	case 0x31: // AND (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.A &= c.Read(addr)
		c.setNZ(c.A)
	case 0x35: // AND zp,X
		c.A &= c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF)
		c.PC++
		c.setNZ(c.A)
	case 0x36: // ROL zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val = val<<1 | carry
		c.Write(addr, val)
		c.setNZ(val)
	case 0x38: // SEC
		c.P |= FlagC
	case 0x39: // AND abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A &= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y))
		c.setNZ(c.A)
	case 0x3D: // AND abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A &= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X))
		c.setNZ(c.A)
	case 0x3E: // ROL abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&0x80 != 0 {
			c.P |= FlagC
		}
		val = val<<1 | carry
		c.Write(addr, val)
		c.setNZ(val)
	case 0x40: // RTI
		c.P = c.pop() | FlagU
		lo := c.pop()
		hi := c.pop()
		c.PC = uint16(lo) | uint16(hi)<<8
	case 0x41: // EOR (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.A ^= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x45: // EOR zp
		c.A ^= c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.A)
	case 0x46: // LSR zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr)
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val >>= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x48: // PHA
		c.push(c.A)
	case 0x49: // EOR #imm
		c.A ^= c.Read(c.PC)
		c.PC++
		c.setNZ(c.A)
	case 0x4A: // LSR A
		c.P &^= FlagC
		if c.A&1 != 0 {
			c.P |= FlagC
		}
		c.A >>= 1
		c.setNZ(c.A)
	case 0x4C: // JMP abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC = uint16(lo) | uint16(hi)<<8
	case 0x4D: // EOR abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A ^= c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0x4E: // LSR abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr)
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val >>= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x50: // BVC
		c.branch(c.P&FlagV == 0)
	case 0x51: // EOR (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.A ^= c.Read(addr)
		c.setNZ(c.A)
	case 0x55: // EOR zp,X
		c.A ^= c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF)
		c.PC++
		c.setNZ(c.A)
	case 0x56: // LSR zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr)
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val >>= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x58: // CLI
		c.P &^= FlagI
	case 0x59: // EOR abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A ^= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y))
		c.setNZ(c.A)
	case 0x5D: // EOR abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A ^= c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X))
		c.setNZ(c.A)
	case 0x5E: // LSR abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr)
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val >>= 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0x60: // RTS
		lo := c.pop()
		hi := c.pop()
		c.PC = (uint16(lo) | uint16(hi)<<8) + 1
	case 0x61: // ADC (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.adc(c.Read(uint16(lo) | uint16(hi)<<8))
	case 0x65: // ADC zp
		c.adc(c.Read(uint16(c.Read(c.PC))))
		c.PC++
	case 0x66: // ROR zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setNZ(val)
	case 0x68: // PLA
		c.A = c.pop()
		c.setNZ(c.A)
	case 0x69: // ADC #imm
		c.adc(c.Read(c.PC))
		c.PC++
	case 0x6A: // ROR A
		carry := c.P & FlagC
		c.P &^= FlagC
		if c.A&1 != 0 {
			c.P |= FlagC
		}
		c.A = c.A>>1 | carry<<7
		c.setNZ(c.A)
	case 0x6C: // JMP (ind)
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		addr := uint16(lo) | uint16(hi)<<8
		c.PC = uint16(c.Read(addr)) | uint16(c.Read((addr&0xFF00)|((addr+1)&0xFF)))<<8
	case 0x6D: // ADC abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.adc(c.Read(uint16(lo) | uint16(hi)<<8))
	case 0x6E: // ROR abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setNZ(val)
	case 0x70: // BVS
		c.branch(c.P&FlagV != 0)
	case 0x71: // ADC (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.adc(c.Read(addr))
	case 0x75: // ADC zp,X
		c.adc(c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF))
		c.PC++
	case 0x76: // ROR zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setNZ(val)
	case 0x78: // SEI
		c.P |= FlagI
	case 0x79: // ADC abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.adc(c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y)))
	case 0x7D: // ADC abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.adc(c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X)))
	case 0x7E: // ROR abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr)
		carry := c.P & FlagC
		c.P &^= FlagC
		if val&1 != 0 {
			c.P |= FlagC
		}
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setNZ(val)
	case 0x81: // STA (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.Write(uint16(lo)|uint16(hi)<<8, c.A)
	case 0x84: // STY zp
		c.Write(uint16(c.Read(c.PC)), c.Y)
		c.PC++
	case 0x85: // STA zp
		c.Write(uint16(c.Read(c.PC)), c.A)
		c.PC++
	case 0x86: // STX zp
		c.Write(uint16(c.Read(c.PC)), c.X)
		c.PC++
	case 0x88: // DEY
		c.Y--
		c.setNZ(c.Y)
	case 0x8A: // TXA
		c.A = c.X
		c.setNZ(c.A)
	case 0x8C: // STY abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Write(uint16(lo)|uint16(hi)<<8, c.Y)
	case 0x8D: // STA abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Write(uint16(lo)|uint16(hi)<<8, c.A)
	case 0x8E: // STX abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Write(uint16(lo)|uint16(hi)<<8, c.X)
	case 0x90: // BCC
		c.branch(c.P&FlagC == 0)
	case 0x91: // STA (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.Write(addr, c.A)
	case 0x94: // STY zp,X
		c.Write(uint16(c.Read(c.PC)+c.X)&0xFF, c.Y)
		c.PC++
	case 0x95: // STA zp,X
		c.Write(uint16(c.Read(c.PC)+c.X)&0xFF, c.A)
		c.PC++
	case 0x96: // STX zp,Y
		c.Write(uint16(c.Read(c.PC)+c.Y)&0xFF, c.X)
		c.PC++
	case 0x98: // TYA
		c.A = c.Y
		c.setNZ(c.A)
	case 0x99: // STA abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Write((uint16(lo)|uint16(hi)<<8)+uint16(c.Y), c.A)
	case 0x9A: // TXS
		c.SP = c.X
	case 0x9D: // STA abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Write((uint16(lo)|uint16(hi)<<8)+uint16(c.X), c.A)
	case 0xA0: // LDY #imm
		c.Y = c.Read(c.PC)
		c.PC++
		c.setNZ(c.Y)
	case 0xA1: // LDA (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.A = c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0xA2: // LDX #imm
		c.X = c.Read(c.PC)
		c.PC++
		c.setNZ(c.X)
	case 0xA4: // LDY zp
		c.Y = c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.Y)
	case 0xA5: // LDA zp
		c.A = c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.A)
	case 0xA6: // LDX zp
		c.X = c.Read(uint16(c.Read(c.PC)))
		c.PC++
		c.setNZ(c.X)
	case 0xA8: // TAY
		c.Y = c.A
		c.setNZ(c.Y)
	case 0xA9: // LDA #imm
		c.A = c.Read(c.PC)
		c.PC++
		c.setNZ(c.A)
	case 0xAA: // TAX
		c.X = c.A
		c.setNZ(c.X)
	case 0xAC: // LDY abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Y = c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.Y)
	case 0xAD: // LDA abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A = c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.A)
	case 0xAE: // LDX abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.X = c.Read(uint16(lo) | uint16(hi)<<8)
		c.setNZ(c.X)
	case 0xB0: // BCS
		c.branch(c.P&FlagC != 0)
	case 0xB1: // LDA (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.A = c.Read(addr)
		c.setNZ(c.A)
	case 0xB4: // LDY zp,X
		c.Y = c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF)
		c.PC++
		c.setNZ(c.Y)
	case 0xB5: // LDA zp,X
		c.A = c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF)
		c.PC++
		c.setNZ(c.A)
	case 0xB6: // LDX zp,Y
		c.X = c.Read(uint16(c.Read(c.PC)+c.Y) & 0xFF)
		c.PC++
		c.setNZ(c.X)
	case 0xB8: // CLV
		c.P &^= FlagV
	case 0xB9: // LDA abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A = c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y))
		c.setNZ(c.A)
	case 0xBA: // TSX
		c.X = c.SP
		c.setNZ(c.X)
	case 0xBC: // LDY abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.Y = c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X))
		c.setNZ(c.Y)
	case 0xBD: // LDA abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.A = c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X))
		c.setNZ(c.A)
	case 0xBE: // LDX abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.X = c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y))
		c.setNZ(c.X)
	case 0xC0: // CPY #imm
		c.cmp(c.Y, c.Read(c.PC))
		c.PC++
	case 0xC1: // CMP (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.cmp(c.A, c.Read(uint16(lo)|uint16(hi)<<8))
	case 0xC4: // CPY zp
		c.cmp(c.Y, c.Read(uint16(c.Read(c.PC))))
		c.PC++
	case 0xC5: // CMP zp
		c.cmp(c.A, c.Read(uint16(c.Read(c.PC))))
		c.PC++
	case 0xC6: // DEC zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xC8: // INY
		c.Y++
		c.setNZ(c.Y)
	case 0xC9: // CMP #imm
		c.cmp(c.A, c.Read(c.PC))
		c.PC++
	case 0xCA: // DEX
		c.X--
		c.setNZ(c.X)
	case 0xCC: // CPY abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.cmp(c.Y, c.Read(uint16(lo)|uint16(hi)<<8))
	case 0xCD: // CMP abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.cmp(c.A, c.Read(uint16(lo)|uint16(hi)<<8))
	case 0xCE: // DEC abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xD0: // BNE
		c.branch(c.P&FlagZ == 0)
	case 0xD1: // CMP (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.cmp(c.A, c.Read(addr))
	case 0xD5: // CMP zp,X
		c.cmp(c.A, c.Read(uint16(c.Read(c.PC)+c.X)&0xFF))
		c.PC++
	case 0xD6: // DEC zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xD8: // CLD
		c.P &^= FlagD
	case 0xD9: // CMP abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.cmp(c.A, c.Read((uint16(lo)|uint16(hi)<<8)+uint16(c.Y)))
	case 0xDD: // CMP abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.cmp(c.A, c.Read((uint16(lo)|uint16(hi)<<8)+uint16(c.X)))
	case 0xDE: // DEC abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xE0: // CPX #imm
		c.cmp(c.X, c.Read(c.PC))
		c.PC++
	case 0xE1: // SBC (ind,X)
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		lo := c.Read(addr)
		hi := c.Read((addr + 1) & 0xFF)
		c.sbc(c.Read(uint16(lo) | uint16(hi)<<8))
	case 0xE4: // CPX zp
		c.cmp(c.X, c.Read(uint16(c.Read(c.PC))))
		c.PC++
	case 0xE5: // SBC zp
		c.sbc(c.Read(uint16(c.Read(c.PC))))
		c.PC++
	case 0xE6: // INC zp
		addr := uint16(c.Read(c.PC))
		c.PC++
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xE8: // INX
		c.X++
		c.setNZ(c.X)
	case 0xE9: // SBC #imm
		c.sbc(c.Read(c.PC))
		c.PC++
	case 0xEA: // NOP
	case 0xEC: // CPX abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.cmp(c.X, c.Read(uint16(lo)|uint16(hi)<<8))
	case 0xED: // SBC abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.sbc(c.Read(uint16(lo) | uint16(hi)<<8))
	case 0xEE: // INC abs
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := uint16(lo) | uint16(hi)<<8
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xF0: // BEQ
		c.branch(c.P&FlagZ != 0)
	case 0xF1: // SBC (ind),Y
		zp := c.Read(c.PC)
		c.PC++
		lo := c.Read(uint16(zp))
		hi := c.Read(uint16(zp+1) & 0xFF)
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.Y)
		c.sbc(c.Read(addr))
	case 0xF5: // SBC zp,X
		c.sbc(c.Read(uint16(c.Read(c.PC)+c.X) & 0xFF))
		c.PC++
	case 0xF6: // INC zp,X
		addr := uint16(c.Read(c.PC)+c.X) & 0xFF
		c.PC++
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setNZ(val)
	case 0xF8: // SED
		c.P |= FlagD
	case 0xF9: // SBC abs,Y
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.sbc(c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.Y)))
	case 0xFD: // SBC abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		c.sbc(c.Read((uint16(lo) | uint16(hi)<<8) + uint16(c.X)))
	case 0xFE: // INC abs,X
		lo := c.Read(c.PC)
		hi := c.Read(c.PC + 1)
		c.PC += 2
		addr := (uint16(lo) | uint16(hi)<<8) + uint16(c.X)
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setNZ(val)
	default:
		return false
	}
	return true
}

func (c *CPU6502) adc(val byte) {
	carry := (c.P & FlagC)
	sum := uint16(c.A) + uint16(val) + uint16(carry)
	c.P &^= FlagC | FlagV | FlagN | FlagZ
	if sum > 255 {
		c.P |= FlagC
	}
	if (c.A^byte(sum))&(val^byte(sum))&0x80 != 0 {
		c.P |= FlagV
	}
	c.A = byte(sum)
	if c.A == 0 {
		c.P |= FlagZ
	}
	if c.A&0x80 != 0 {
		c.P |= FlagN
	}
}

func (c *CPU6502) sbc(val byte) {
	carry := (c.P & FlagC) ^ 1
	diff := uint16(c.A) - uint16(val) - uint16(carry)
	c.P &^= FlagC | FlagV | FlagN | FlagZ
	if diff < 256 {
		c.P |= FlagC
	}
	if (c.A^val)&(c.A^byte(diff))&0x80 != 0 {
		c.P |= FlagV
	}
	c.A = byte(diff)
	if c.A == 0 {
		c.P |= FlagZ
	}
	if c.A&0x80 != 0 {
		c.P |= FlagN
	}
}

func (c *CPU6502) cmp(a, b byte) {
	result := uint16(a) - uint16(b)
	c.P &^= FlagC | FlagN | FlagZ
	if result < 256 {
		c.P |= FlagC
	}
	if byte(result) == 0 {
		c.P |= FlagZ
	}
	if byte(result)&0x80 != 0 {
		c.P |= FlagN
	}
}

func (c *CPU6502) Call(addr uint16) {
	c.push(0xFF)
	c.push(0xFE)
	c.Memory[0xFFFF] = 0x00
	c.PC = addr
	for c.PC != 0xFFFF {
		if !c.Step() {
			break
		}
	}
}

func (c *CPU6502) RunFrames(playAddr uint16, frames int) []SIDWrite {
	c.SIDWrites = nil
	c.CurrentFrame = 0
	for i := 0; i < frames; i++ {
		c.CurrentFrame = i
		c.Call(playAddr)
	}
	return c.SIDWrites
}
