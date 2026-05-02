// xterm-mirror.js — per-pane VT state tracker for bmux HeadlessMirrorManager.
// Reads JSON line requests from stdin; writes JSON line responses to stdout.
// Uses a minimal VT100 state tracker (pure Node, no npm dependencies) to
// track the visible grid. Snapshot returns: ESC[2J ESC[H + grid lines joined
// with CRLF — suitable for writing directly to an xterm.js client.

'use strict';

const readline = require('readline');

// VTTerminal tracks a cols×rows character grid with basic VT100 support:
// printable characters, CR, LF, BS, TAB, and a subset of CSI sequences.
class VTTerminal {
  constructor(cols, rows) {
    this.cols = cols;
    this.rows = rows;
    this.cx = 0; // cursor column (0-based)
    this.cy = 0; // cursor row (0-based)
    this.grid = this._makeGrid(cols, rows);
    this._buf = ''; // escape sequence accumulation buffer
    this._altGrid = null; // alt-screen grid
    this._altCx = 0;
    this._altCy = 0;
    this._onAlt = false;
  }

  _makeGrid(cols, rows) {
    const g = [];
    for (let r = 0; r < rows; r++) {
      g.push(new Array(cols).fill(' '));
    }
    return g;
  }

  // resize adjusts the grid to new dimensions, preserving content.
  resize(cols, rows) {
    const newGrid = this._makeGrid(cols, rows);
    const copyRows = Math.min(rows, this.rows);
    const copyCols = Math.min(cols, this.cols);
    for (let r = 0; r < copyRows; r++) {
      for (let c = 0; c < copyCols; c++) {
        newGrid[r][c] = this.grid[r][c];
      }
    }
    this.cols = cols;
    this.rows = rows;
    this.grid = newGrid;
    this.cx = Math.min(this.cx, cols - 1);
    this.cy = Math.min(this.cy, rows - 1);
  }

  // write processes a chunk of terminal output bytes.
  write(data) {
    for (let i = 0; i < data.length; i++) {
      const ch = data[i];
      if (this._buf.length > 0) {
        this._buf += ch;
        if (this._handleEscape()) {
          this._buf = '';
        } else if (this._buf.length > 64) {
          // Give up on malformed sequences.
          this._buf = '';
        }
        continue;
      }
      if (ch === '\x1b') {
        this._buf = '\x1b';
        continue;
      }
      this._printChar(ch);
    }
  }

  _handleEscape() {
    const buf = this._buf;
    if (buf.length < 2) return false;

    if (buf[1] === '[') {
      // CSI sequence — wait for terminator (letter).
      if (buf.length < 3) return false;
      const last = buf[buf.length - 1];
      if (/[A-Za-z]/.test(last)) {
        this._handleCSI(buf.slice(2, -1), last);
        return true;
      }
      // Still accumulating.
      return false;
    }

    if (buf[1] === '?') {
      // Private mode — accumulate until letter.
      if (buf.length < 3) return false;
      const last = buf[buf.length - 1];
      if (/[hlH]/.test(last)) {
        this._handlePrivate(buf.slice(2, -1), last);
        return true;
      }
      return false;
    }

    if (buf[1] === '(') {
      // Character set designation — ignore, consume next char.
      if (buf.length < 3) return false;
      return true;
    }

    // Single char escape sequences.
    switch (buf[1]) {
      case 'M': // reverse index
        if (this.cy > 0) this.cy--;
        return true;
      case 'c': // full reset
        this.grid = this._makeGrid(this.cols, this.rows);
        this.cx = 0; this.cy = 0;
        return true;
    }
    // Unknown — discard.
    return true;
  }

  _handleCSI(params, cmd) {
    const nums = params ? params.split(';').map(s => parseInt(s, 10) || 0) : [0];
    const n = nums[0];
    switch (cmd) {
      case 'A': this.cy = Math.max(0, this.cy - (n || 1)); break;
      case 'B': this.cy = Math.min(this.rows - 1, this.cy + (n || 1)); break;
      case 'C': this.cx = Math.min(this.cols - 1, this.cx + (n || 1)); break;
      case 'D': this.cx = Math.max(0, this.cx - (n || 1)); break;
      case 'H': case 'f': {
        this.cy = Math.max(0, (nums[0] || 1) - 1);
        this.cx = Math.max(0, (nums[1] || 1) - 1);
        this.cy = Math.min(this.rows - 1, this.cy);
        this.cx = Math.min(this.cols - 1, this.cx);
        break;
      }
      case 'J': {
        if (n === 0) { // clear from cursor to end
          for (let c = this.cx; c < this.cols; c++) this.grid[this.cy][c] = ' ';
          for (let r = this.cy + 1; r < this.rows; r++) this.grid[r] = new Array(this.cols).fill(' ');
        } else if (n === 1) { // clear from beginning to cursor
          for (let r = 0; r < this.cy; r++) this.grid[r] = new Array(this.cols).fill(' ');
          for (let c = 0; c <= this.cx; c++) this.grid[this.cy][c] = ' ';
        } else if (n === 2 || n === 3) { // clear entire screen
          this.grid = this._makeGrid(this.cols, this.rows);
          this.cx = 0; this.cy = 0;
        }
        break;
      }
      case 'K': {
        if (n === 0) for (let c = this.cx; c < this.cols; c++) this.grid[this.cy][c] = ' ';
        else if (n === 1) for (let c = 0; c <= this.cx; c++) this.grid[this.cy][c] = ' ';
        else if (n === 2) this.grid[this.cy] = new Array(this.cols).fill(' ');
        break;
      }
      case 'L': { // insert lines
        const count = n || 1;
        for (let i = 0; i < count; i++) {
          this.grid.splice(this.cy, 0, new Array(this.cols).fill(' '));
          if (this.grid.length > this.rows) this.grid.pop();
        }
        break;
      }
      case 'M': { // delete lines
        const count = n || 1;
        for (let i = 0; i < count; i++) {
          this.grid.splice(this.cy, 1);
          this.grid.push(new Array(this.cols).fill(' '));
        }
        break;
      }
      case 'P': { // delete chars
        const count = n || 1;
        this.grid[this.cy].splice(this.cx, count);
        while (this.grid[this.cy].length < this.cols) this.grid[this.cy].push(' ');
        break;
      }
      case 'S': this._scrollUp(n || 1); break;
      case 'T': this._scrollDown(n || 1); break;
      case 'd': this.cy = Math.min(this.rows - 1, Math.max(0, (n || 1) - 1)); break;
      case 'G': this.cx = Math.min(this.cols - 1, Math.max(0, (n || 1) - 1)); break;
      case 'm': break; // SGR — ignore colors/attrs
      case 'r': break; // scroll region — ignore
      case 's': break; // save cursor
      case 'u': break; // restore cursor
      case 'h': case 'l': break; // mode set/reset — ignore
    }
  }

  _handlePrivate(params, cmd) {
    const nums = params ? params.split(';').map(s => parseInt(s, 10) || 0) : [0];
    for (const n of nums) {
      if (n === 1049) {
        if (cmd === 'h') {
          // Enter alt screen.
          this._altGrid = this.grid.map(row => [...row]);
          this._altCx = this.cx;
          this._altCy = this.cy;
          this._onAlt = true;
          this.grid = this._makeGrid(this.cols, this.rows);
          this.cx = 0; this.cy = 0;
        } else if (cmd === 'l') {
          // Exit alt screen.
          if (this._altGrid) {
            this.grid = this._altGrid;
            this.cx = this._altCx;
            this.cy = this._altCy;
            this._altGrid = null;
          }
          this._onAlt = false;
        }
      }
      // Other private modes ignored.
    }
  }

  _printChar(ch) {
    const code = ch.charCodeAt(0);
    switch (code) {
      case 0x07: return; // BEL
      case 0x08: if (this.cx > 0) this.cx--; return; // BS
      case 0x09: this.cx = Math.min(this.cols - 1, (Math.floor(this.cx / 8) + 1) * 8); return; // TAB
      case 0x0a: case 0x0b: case 0x0c: // LF/VT/FF
        if (this.cy < this.rows - 1) {
          this.cy++;
        } else {
          this._scrollUp(1);
        }
        return;
      case 0x0d: this.cx = 0; return; // CR
      case 0x0e: case 0x0f: return; // SO/SI — ignore
    }
    if (code < 0x20) return; // other control chars ignored
    if (this.cx >= this.cols) {
      this.cx = 0;
      if (this.cy < this.rows - 1) {
        this.cy++;
      } else {
        this._scrollUp(1);
      }
    }
    this.grid[this.cy][this.cx] = ch;
    this.cx++;
  }

  _scrollUp(n) {
    for (let i = 0; i < n; i++) {
      this.grid.shift();
      this.grid.push(new Array(this.cols).fill(' '));
    }
  }

  _scrollDown(n) {
    for (let i = 0; i < n; i++) {
      this.grid.pop();
      this.grid.unshift(new Array(this.cols).fill(' '));
    }
  }

  // serializeAt renders the grid content at the given cols/rows viewport
  // without permanently mutating the stored grid dimensions.
  // Returns: ESC[2J ESC[H + content lines joined with CRLF.
  serializeAt(cols, rows) {
    // Flatten all stored grid content into one continuous string.
    const flat = this.grid.map(row => row.join('').replace(/ +$/, '')).join('\n');
    // Re-flow the flat content into cols-wide lines.
    const outLines = [];
    let remaining = flat;
    while (remaining.length > 0 && outLines.length < rows) {
      const nl = remaining.indexOf('\n');
      let segment;
      if (nl === -1) {
        segment = remaining;
        remaining = '';
      } else {
        segment = remaining.slice(0, nl);
        remaining = remaining.slice(nl + 1);
      }
      // Wrap segment at cols.
      if (segment.length === 0) {
        outLines.push('');
      } else {
        for (let i = 0; i < segment.length && outLines.length < rows; i += cols) {
          outLines.push(segment.slice(i, i + cols));
        }
      }
    }
    // Trim trailing empty lines.
    let last = outLines.length - 1;
    while (last > 0 && outLines[last] === '') last--;
    const content = outLines.slice(0, last + 1).join('\r\n');
    return '\x1b[2J\x1b[H' + content;
  }
}

// Map<paneID, VTTerminal>
const panes = new Map();

function getOrCreate(paneID, cols, rows) {
  if (!panes.has(paneID)) {
    panes.set(paneID, new VTTerminal(cols || 220, rows || 50));
  }
  return panes.get(paneID);
}

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

rl.on('line', (line) => {
  let req;
  try {
    req = JSON.parse(line);
  } catch (e) {
    // Malformed request — ignore.
    return;
  }

  const { id, op, pane } = req;

  try {
    switch (op) {
      case 'write': {
        const data = Buffer.from(req.data || '', 'base64').toString('binary');
        getOrCreate(pane, 220, 50).write(data);
        respond({ id, ok: true });
        break;
      }
      case 'snapshot': {
        const cols = req.cols || 80;
        const rows = req.rows || 24;
        if (!panes.has(pane)) {
          respond({ id, ok: true, data: '' });
          break;
        }
        const term = panes.get(pane);
        const ansi = term.serializeAt(cols, rows);
        respond({ id, ok: true, data: Buffer.from(ansi, 'binary').toString('base64') });
        break;
      }
      case 'resize': {
        if (panes.has(pane)) {
          panes.get(pane).resize(req.cols || 80, req.rows || 24);
        }
        respond({ id, ok: true });
        break;
      }
      case 'destroy': {
        panes.delete(pane);
        respond({ id, ok: true });
        break;
      }
      default:
        respond({ id, ok: false, error: `unknown op: ${op}` });
    }
  } catch (e) {
    respond({ id, ok: false, error: String(e) });
  }
});

rl.on('close', () => process.exit(0));

function respond(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}
