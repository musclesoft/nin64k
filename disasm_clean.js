#!/usr/bin/env node
// Clean disassembler for SounDemoN "Ninjas"
// Produces readable assembly with named labels and proper data sections

const fs = require('fs');
const path = require('path');

const data = fs.readFileSync(path.join(__dirname, 'original/nin-soundemon.prg'));
const loadAddr = data[0] | (data[1] << 8);
const code = data.slice(2);
const BASE = loadAddr;
const END = BASE + code.length;

// Known labels with human-readable names (verified addresses)
const LABELS = {
  0x0812: 'start',
  0x083D: 'main_loop',
  0x0856: 'do_load_next',
  0x086A: 'setup_irq',
  0x0895: 'irq_handler',
  0x08C0: 'play_tick',
  0x08CF: 'check_countdown',
  0x08FE: 'play_done',
  0x08FF: 'clear_screen',
  0x093E: 'print_msg',
  0x094D: 'print_string',
  0x09B4: 'print_done',
  0x09B5: 'load_and_init',
  0x09D4: 'init_buf1',
  0x09E4: 'init_buf2',
  0x09F4: 'load_error',
  0x0A0D: 'menu_select',
  0x0A22: 'menu_loop',
  0x0A5F: 'menu_done',
  0x0A75: 'dev_chars',
  0x0A7D: 'msg_menu',
  0x0BED: 'part_times',
  0x0C00: 'load_d0',
  0x0C03: 'load_tune',
  0x0C06: 'load_d0_impl',
  0x0C6F: 'setup_drive',
  0x0C84: 'load_tune_impl',
  0x0CB8: 'fastload_byte',
  0x0CCB: 'fastload_getbit',
  0x0CF4: 'fastload_sendbyte',
  0x0D1D: 'drivecode',
  0x0E50: 'unused_space',
  0x0E60: 'decompress',
  0x0F3D: 'decomp_getbyte',
  0x0F54: 'decomp_putbyte',
  0x0F6D: 'decomp_vars',
  0x0F80: 'show_info',
  0x0FC0: 'init_game',
  0x0FDF: 'init_timing_data',
};

// Data regions (start, end, type)
const DATA_REGIONS = [
  { start: 0x0801, end: 0x0812, type: 'basic', name: 'BASIC stub: SYS 2066' },
  { start: 0x0A75, end: 0x0A7D, type: 'bytes', name: 'Device number display chars' },
  { start: 0x0A7D, end: 0x0BED, type: 'text', name: 'Menu and message text' },
  { start: 0x0BED, end: 0x0BFF, type: 'words', name: 'Part timing data (9 parts x 2 bytes)' },
  { start: 0x0C79, end: 0x0C84, type: 'bytes', name: 'Fastload parameters' },
  { start: 0x0D1D, end: 0x0E50, type: 'bytes', name: 'Drive code (uploaded to 1541)' },
  { start: 0x0E50, end: 0x0E60, type: 'bytes', name: 'Padding/unused' },
  { start: 0x0F6D, end: 0x0F80, type: 'bytes', name: 'Decompression variables' },
  { start: 0x0FAB, end: 0x0FC0, type: 'bytes', name: 'Padding/unused' },
  { start: 0x0FDF, end: 0x1000, type: 'bytes', name: 'Initial part timing data' },
];

const opcodeTable = {
  0x00: { mn: 'BRK', len: 1 },
  0x01: { mn: 'ORA', len: 2, mode: 'indx' },
  0x05: { mn: 'ORA', len: 2, mode: 'zp' },
  0x06: { mn: 'ASL', len: 2, mode: 'zp' },
  0x08: { mn: 'PHP', len: 1 },
  0x09: { mn: 'ORA', len: 2, mode: 'imm' },
  0x0A: { mn: 'ASL', len: 1, mode: 'acc' },
  0x0D: { mn: 'ORA', len: 3, mode: 'abs' },
  0x0E: { mn: 'ASL', len: 3, mode: 'abs' },
  0x10: { mn: 'BPL', len: 2, mode: 'rel' },
  0x11: { mn: 'ORA', len: 2, mode: 'indy' },
  0x15: { mn: 'ORA', len: 2, mode: 'zpx' },
  0x16: { mn: 'ASL', len: 2, mode: 'zpx' },
  0x18: { mn: 'CLC', len: 1 },
  0x19: { mn: 'ORA', len: 3, mode: 'absy' },
  0x1D: { mn: 'ORA', len: 3, mode: 'absx' },
  0x1E: { mn: 'ASL', len: 3, mode: 'absx' },
  0x20: { mn: 'JSR', len: 3, mode: 'abs' },
  0x21: { mn: 'AND', len: 2, mode: 'indx' },
  0x24: { mn: 'BIT', len: 2, mode: 'zp' },
  0x25: { mn: 'AND', len: 2, mode: 'zp' },
  0x26: { mn: 'ROL', len: 2, mode: 'zp' },
  0x28: { mn: 'PLP', len: 1 },
  0x29: { mn: 'AND', len: 2, mode: 'imm' },
  0x2A: { mn: 'ROL', len: 1, mode: 'acc' },
  0x2C: { mn: 'BIT', len: 3, mode: 'abs' },
  0x2D: { mn: 'AND', len: 3, mode: 'abs' },
  0x2E: { mn: 'ROL', len: 3, mode: 'abs' },
  0x30: { mn: 'BMI', len: 2, mode: 'rel' },
  0x31: { mn: 'AND', len: 2, mode: 'indy' },
  0x35: { mn: 'AND', len: 2, mode: 'zpx' },
  0x36: { mn: 'ROL', len: 2, mode: 'zpx' },
  0x38: { mn: 'SEC', len: 1 },
  0x39: { mn: 'AND', len: 3, mode: 'absy' },
  0x3D: { mn: 'AND', len: 3, mode: 'absx' },
  0x3E: { mn: 'ROL', len: 3, mode: 'absx' },
  0x40: { mn: 'RTI', len: 1 },
  0x41: { mn: 'EOR', len: 2, mode: 'indx' },
  0x45: { mn: 'EOR', len: 2, mode: 'zp' },
  0x46: { mn: 'LSR', len: 2, mode: 'zp' },
  0x48: { mn: 'PHA', len: 1 },
  0x49: { mn: 'EOR', len: 2, mode: 'imm' },
  0x4A: { mn: 'LSR', len: 1, mode: 'acc' },
  0x4C: { mn: 'JMP', len: 3, mode: 'abs' },
  0x4D: { mn: 'EOR', len: 3, mode: 'abs' },
  0x4E: { mn: 'LSR', len: 3, mode: 'abs' },
  0x50: { mn: 'BVC', len: 2, mode: 'rel' },
  0x51: { mn: 'EOR', len: 2, mode: 'indy' },
  0x55: { mn: 'EOR', len: 2, mode: 'zpx' },
  0x56: { mn: 'LSR', len: 2, mode: 'zpx' },
  0x58: { mn: 'CLI', len: 1 },
  0x59: { mn: 'EOR', len: 3, mode: 'absy' },
  0x5D: { mn: 'EOR', len: 3, mode: 'absx' },
  0x5E: { mn: 'LSR', len: 3, mode: 'absx' },
  0x60: { mn: 'RTS', len: 1 },
  0x61: { mn: 'ADC', len: 2, mode: 'indx' },
  0x65: { mn: 'ADC', len: 2, mode: 'zp' },
  0x66: { mn: 'ROR', len: 2, mode: 'zp' },
  0x68: { mn: 'PLA', len: 1 },
  0x69: { mn: 'ADC', len: 2, mode: 'imm' },
  0x6A: { mn: 'ROR', len: 1, mode: 'acc' },
  0x6C: { mn: 'JMP', len: 3, mode: 'ind' },
  0x6D: { mn: 'ADC', len: 3, mode: 'abs' },
  0x6E: { mn: 'ROR', len: 3, mode: 'abs' },
  0x70: { mn: 'BVS', len: 2, mode: 'rel' },
  0x71: { mn: 'ADC', len: 2, mode: 'indy' },
  0x75: { mn: 'ADC', len: 2, mode: 'zpx' },
  0x76: { mn: 'ROR', len: 2, mode: 'zpx' },
  0x78: { mn: 'SEI', len: 1 },
  0x79: { mn: 'ADC', len: 3, mode: 'absy' },
  0x7D: { mn: 'ADC', len: 3, mode: 'absx' },
  0x7E: { mn: 'ROR', len: 3, mode: 'absx' },
  0x81: { mn: 'STA', len: 2, mode: 'indx' },
  0x84: { mn: 'STY', len: 2, mode: 'zp' },
  0x85: { mn: 'STA', len: 2, mode: 'zp' },
  0x86: { mn: 'STX', len: 2, mode: 'zp' },
  0x88: { mn: 'DEY', len: 1 },
  0x8A: { mn: 'TXA', len: 1 },
  0x8C: { mn: 'STY', len: 3, mode: 'abs' },
  0x8D: { mn: 'STA', len: 3, mode: 'abs' },
  0x8E: { mn: 'STX', len: 3, mode: 'abs' },
  0x90: { mn: 'BCC', len: 2, mode: 'rel' },
  0x91: { mn: 'STA', len: 2, mode: 'indy' },
  0x94: { mn: 'STY', len: 2, mode: 'zpx' },
  0x95: { mn: 'STA', len: 2, mode: 'zpx' },
  0x96: { mn: 'STX', len: 2, mode: 'zpy' },
  0x98: { mn: 'TYA', len: 1 },
  0x99: { mn: 'STA', len: 3, mode: 'absy' },
  0x9A: { mn: 'TXS', len: 1 },
  0x9D: { mn: 'STA', len: 3, mode: 'absx' },
  0xA0: { mn: 'LDY', len: 2, mode: 'imm' },
  0xA1: { mn: 'LDA', len: 2, mode: 'indx' },
  0xA2: { mn: 'LDX', len: 2, mode: 'imm' },
  0xA4: { mn: 'LDY', len: 2, mode: 'zp' },
  0xA5: { mn: 'LDA', len: 2, mode: 'zp' },
  0xA6: { mn: 'LDX', len: 2, mode: 'zp' },
  0xA8: { mn: 'TAY', len: 1 },
  0xA9: { mn: 'LDA', len: 2, mode: 'imm' },
  0xAA: { mn: 'TAX', len: 1 },
  0xAC: { mn: 'LDY', len: 3, mode: 'abs' },
  0xAD: { mn: 'LDA', len: 3, mode: 'abs' },
  0xAE: { mn: 'LDX', len: 3, mode: 'abs' },
  0xB0: { mn: 'BCS', len: 2, mode: 'rel' },
  0xB1: { mn: 'LDA', len: 2, mode: 'indy' },
  0xB4: { mn: 'LDY', len: 2, mode: 'zpx' },
  0xB5: { mn: 'LDA', len: 2, mode: 'zpx' },
  0xB6: { mn: 'LDX', len: 2, mode: 'zpy' },
  0xB8: { mn: 'CLV', len: 1 },
  0xB9: { mn: 'LDA', len: 3, mode: 'absy' },
  0xBA: { mn: 'TSX', len: 1 },
  0xBC: { mn: 'LDY', len: 3, mode: 'absx' },
  0xBD: { mn: 'LDA', len: 3, mode: 'absx' },
  0xBE: { mn: 'LDX', len: 3, mode: 'absy' },
  0xC0: { mn: 'CPY', len: 2, mode: 'imm' },
  0xC1: { mn: 'CMP', len: 2, mode: 'indx' },
  0xC4: { mn: 'CPY', len: 2, mode: 'zp' },
  0xC5: { mn: 'CMP', len: 2, mode: 'zp' },
  0xC6: { mn: 'DEC', len: 2, mode: 'zp' },
  0xC8: { mn: 'INY', len: 1 },
  0xC9: { mn: 'CMP', len: 2, mode: 'imm' },
  0xCA: { mn: 'DEX', len: 1 },
  0xCC: { mn: 'CPY', len: 3, mode: 'abs' },
  0xCD: { mn: 'CMP', len: 3, mode: 'abs' },
  0xCE: { mn: 'DEC', len: 3, mode: 'abs' },
  0xD0: { mn: 'BNE', len: 2, mode: 'rel' },
  0xD1: { mn: 'CMP', len: 2, mode: 'indy' },
  0xD5: { mn: 'CMP', len: 2, mode: 'zpx' },
  0xD6: { mn: 'DEC', len: 2, mode: 'zpx' },
  0xD8: { mn: 'CLD', len: 1 },
  0xD9: { mn: 'CMP', len: 3, mode: 'absy' },
  0xDD: { mn: 'CMP', len: 3, mode: 'absx' },
  0xDE: { mn: 'DEC', len: 3, mode: 'absx' },
  0xE0: { mn: 'CPX', len: 2, mode: 'imm' },
  0xE1: { mn: 'SBC', len: 2, mode: 'indx' },
  0xE4: { mn: 'CPX', len: 2, mode: 'zp' },
  0xE5: { mn: 'SBC', len: 2, mode: 'zp' },
  0xE6: { mn: 'INC', len: 2, mode: 'zp' },
  0xE8: { mn: 'INX', len: 1 },
  0xE9: { mn: 'SBC', len: 2, mode: 'imm' },
  0xEA: { mn: 'NOP', len: 1 },
  0xEC: { mn: 'CPX', len: 3, mode: 'abs' },
  0xED: { mn: 'SBC', len: 3, mode: 'abs' },
  0xEE: { mn: 'INC', len: 3, mode: 'abs' },
  0xF0: { mn: 'BEQ', len: 2, mode: 'rel' },
  0xF1: { mn: 'SBC', len: 2, mode: 'indy' },
  0xF5: { mn: 'SBC', len: 2, mode: 'zpx' },
  0xF6: { mn: 'INC', len: 2, mode: 'zpx' },
  0xF8: { mn: 'SED', len: 1 },
  0xF9: { mn: 'SBC', len: 3, mode: 'absy' },
  0xFD: { mn: 'SBC', len: 3, mode: 'absx' },
  0xFE: { mn: 'INC', len: 3, mode: 'absx' },
};

function hex(n, w = 2) {
  return '$' + n.toString(16).toUpperCase().padStart(w, '0');
}

function getLabel(addr) {
  return LABELS[addr] || null;
}

function isInDataRegion(addr) {
  for (const r of DATA_REGIONS) {
    if (addr >= r.start && addr < r.end) return r;
  }
  return null;
}

function formatOperand(pc, info) {
  const off = pc - BASE;
  const b1 = code[off + 1] || 0;
  const b2 = code[off + 2] || 0;

  switch (info.mode) {
    case 'imm': return '#' + hex(b1);
    case 'zp': return hex(b1);
    case 'zpx': return hex(b1) + ',x';
    case 'zpy': return hex(b1) + ',y';
    case 'abs': {
      const addr = b1 | (b2 << 8);
      const label = getLabel(addr);
      return label || hex(addr, 4);
    }
    case 'absx': {
      const addr = b1 | (b2 << 8);
      const label = getLabel(addr);
      return (label || hex(addr, 4)) + ',x';
    }
    case 'absy': {
      const addr = b1 | (b2 << 8);
      const label = getLabel(addr);
      return (label || hex(addr, 4)) + ',y';
    }
    case 'ind': {
      const addr = b1 | (b2 << 8);
      return '(' + hex(addr, 4) + ')';
    }
    case 'indx': return '(' + hex(b1) + ',x)';
    case 'indy': return '(' + hex(b1) + '),y';
    case 'rel': {
      const target = pc + 2 + (b1 > 127 ? b1 - 256 : b1);
      const label = getLabel(target);
      return label || hex(target, 4);
    }
    case 'acc': return 'a';
    default: return '';
  }
}

function outputDataRegion(region, out) {
  const startOff = region.start - BASE;
  const endOff = region.end - BASE;

  out.push('');
  out.push('; ' + '-'.repeat(70));
  out.push('; ' + region.name);
  out.push('; ' + '-'.repeat(70));

  if (region.type === 'basic') {
    // Output BASIC stub
    out.push('basic_stub:');
    out.push('        .word   $0810               ; Pointer to next BASIC line');
    out.push('        .word   8580                ; Line number');
    out.push('        .byte   $9E                 ; SYS token');
    out.push('        .byte   "2066 NIN!"         ; SYS address + decoration');
    out.push('        .byte   $00                 ; End of line');
    out.push('        .word   $0000               ; End of BASIC program');
  } else if (region.type === 'text') {
    // Output as mixed text/bytes
    let i = startOff;
    while (i < endOff) {
      const addr = BASE + i;
      const label = getLabel(addr);
      if (label) out.push(label + ':');

      // Try to find a string (excluding quotes which can't be in ca65 strings)
      let str = '';
      let strStart = i;
      while (i < endOff && code[i] >= 0x20 && code[i] < 0x80 && code[i] !== 0x22) {
        str += String.fromCharCode(code[i]);
        i++;
      }
      if (str.length > 3) {
        out.push('        .byte   "' + str + '"');
      } else {
        i = strStart;
        // Output as hex bytes
        const bytes = [];
        for (let j = 0; j < 16 && i < endOff; j++, i++) {
          bytes.push(hex(code[i]));
        }
        out.push('        .byte   ' + bytes.join(', '));
      }
      // Handle control chars
      while (i < endOff && (code[i] < 0x20 || code[i] >= 0x80)) {
        if (code[i] === 0x0D) {
          out.push('        .byte   $0D                     ; CR');
          i++;
        } else if (code[i] === 0x00) {
          out.push('        .byte   $00                     ; End of string');
          i++;
        } else {
          out.push('        .byte   ' + hex(code[i]));
          i++;
        }
      }
    }
  } else if (region.type === 'words') {
    for (let i = startOff; i < endOff; i += 2) {
      const addr = BASE + i;
      const label = getLabel(addr);
      if (label) out.push(label + ':');
      const word = code[i] | (code[i + 1] << 8);
      out.push('        .word   ' + hex(word, 4));
    }
  } else {
    // Output as hex bytes, 16 per line
    for (let i = startOff; i < endOff; ) {
      const addr = BASE + i;
      const label = getLabel(addr);
      if (label) out.push(label + ':');

      const bytes = [];
      const lineStart = i;
      for (let j = 0; j < 16 && i < endOff; j++, i++) {
        bytes.push(hex(code[i]));
      }
      out.push('        .byte   ' + bytes.join(', '));
    }
  }
}

// Generate output
const out = [];
out.push('; ============================================================================');
out.push('; SounDemoN "Ninjas" - Clean Disassembly');
out.push('; ============================================================================');
out.push(';');
out.push('; Original file: nin-soundemo');
out.push('; Load address:  $0801');
out.push('; Size:          ' + code.length + ' bytes');
out.push(';');
out.push('; Memory layout:');
out.push(';   $0801-$080C  BASIC stub');
out.push(';   $080D-$0A74  Main code');
out.push(';   $0A75-$0BEC  Menu text and data');
out.push(';   $0BED-$0BFF  Part timing data');
out.push(';   $0C00-$0D1C  Disk loader code');
out.push(';   $0D1D-$0E4F  1541 drive code');
out.push(';   $0E50-$0E5F  Free space (for patches)');
out.push(';   $0E60-$0F7F  Decompression routine');
out.push(';   $0F80-$0FFF  Info screen and init');
out.push(';');
out.push('; Key variables:');
out.push(';   $78 - Selected part from menu');
out.push(';   $79 - Load next part flag (non-zero = load)');
out.push(';   $7B - Current part number (1-9)');
out.push(';');
out.push('; Tune buffers:');
out.push(';   $1000 - Buffer 1 (odd parts: 1,3,5,7,9)');
out.push(';   $7000 - Buffer 2 (even parts: 2,4,6,8)');
out.push(';');
out.push('; ============================================================================');
out.push('');
out.push('.setcpu "6502"');
out.push('');
out.push('; Zero page');
out.push('zp_selected     = $78');
out.push('zp_load_flag    = $79');
out.push('zp_part_num     = $7B');
out.push('zp_ptr_lo       = $8C');
out.push('zp_ptr_hi       = $8D');
out.push('');
out.push('; Hardware');
out.push('VIC_D011        = $D011');
out.push('VIC_D012        = $D012');
out.push('VIC_D018        = $D018');
out.push('VIC_D019        = $D019');
out.push('VIC_D01A        = $D01A');
out.push('VIC_D020        = $D020');
out.push('VIC_D021        = $D021');
out.push('CIA1_DC0D       = $DC0D');
out.push('CIA2_DD00       = $DD00');
out.push('');
out.push('; KERNAL');
out.push('SCNKEY          = $FF9F');
out.push('GETIN           = $FFE4');
out.push('CHROUT          = $FFD2');
out.push('IRQ_RETURN      = $EA31');
out.push('');
out.push('; Tune entry points');
out.push('TUNE1_INIT      = $1000');
out.push('TUNE1_PLAY      = $1003');
out.push('TUNE2_INIT      = $7000');
out.push('TUNE2_PLAY      = $7003');
out.push('');
out.push('.segment "LOADADDR"');
out.push('        .word   $0801');
out.push('');
out.push('.segment "CODE"');
out.push('');

// First pass: collect all branch/jump targets
const branchTargets = new Set();
let scanPc = 0x0801;
while (scanPc < END) {
  const dataRegion = isInDataRegion(scanPc);
  if (dataRegion) {
    scanPc = dataRegion.end;
    continue;
  }
  const off = scanPc - BASE;
  if (off >= code.length) break;
  const opcode = code[off];
  const info = opcodeTable[opcode];
  if (!info) { scanPc++; continue; }
  if (info.mode === 'rel') {
    const b1 = code[off + 1] || 0;
    const target = scanPc + 2 + (b1 > 127 ? b1 - 256 : b1);
    if (!LABELS[target]) branchTargets.add(target);
  }
  scanPc += info.len;
}

// Add auto-labels for branch targets
for (const target of branchTargets) {
  LABELS[target] = `L${target.toString(16).toUpperCase()}`;
}

// Main disassembly - start at beginning, data regions handle BASIC stub
let pc = 0x0801;
let currentDataRegion = null;

while (pc < END) {
  // Check for data region
  const dataRegion = isInDataRegion(pc);
  if (dataRegion && dataRegion !== currentDataRegion) {
    currentDataRegion = dataRegion;
    outputDataRegion(dataRegion, out);
    pc = dataRegion.end;
    continue;
  }

  if (dataRegion) {
    pc++;
    continue;
  }

  currentDataRegion = null;

  // Output label if exists
  const label = getLabel(pc);
  if (label) {
    out.push('');
    out.push('; ----------------------------------------------------------------------------');
    out.push(label + ':');
  }

  const off = pc - BASE;
  if (off >= code.length) break;

  const opcode = code[off];
  const info = opcodeTable[opcode];

  if (!info || off + info.len > code.length) {
    out.push('        .byte   ' + hex(opcode));
    pc++;
    continue;
  }

  const operand = formatOperand(pc, info);
  const instr = operand ? `${info.mn.toLowerCase().padEnd(8)}${operand}` : info.mn.toLowerCase();
  out.push('        ' + instr);
  pc += info.len;
}

console.log(out.join('\n'));
