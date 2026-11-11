// Read evidence for #93: which workspace files the reviewer examined, derived
// from the tool calls the adapters already see. It records repository-relative
// paths only. File content, command output, and command text never leave here.
//
// The result is a lower bound. Command parsing recognises the common readers
// and git forms; a file read some other way (a search tool, a script) is not
// counted, and a provider may truncate long output it was shown.
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const prefix = 'BOSUN_READS_V1 ';
// The worker drops any control line over 64 KiB, so stay well under it.
const budget = 56 * 1024;
const maxPath = 512;
const maxCommands = 100000;

const readers = new Set(['cat', 'nl', 'head', 'tail', 'sed', 'awk', 'less', 'more', 'bat', 'batcat', 'view']);
const shells = new Set(['sh', 'bash', 'zsh', 'dash']);
// Diff options that print names or numbers but no content.
const noContent = /^(-s|--(stat|numstat|shortstat|name-only|name-status|summary|dirstat|compact-summary|quiet|raw|no-patch)(=|$))/;

// tokenize splits a shell script into simple commands of words. It handles
// quotes, backslashes, and the separators ; & | and newlines. It is not a shell:
// expansions are kept literally, which only ever makes a path not match.
function tokenize(script) {
  const commands = [];
  let words = [];
  let word = '';
  let inWord = false;
  let quote = '';
  // A command after || runs only when the one before it failed, and a
  // successful exit says nothing about whether it ran, so it earns no credit.
  let skipped = false;
  const endWord = () => { if (inWord) words.push(word); word = ''; inWord = false; };
  const endCommand = () => { endWord(); if (words.length && !skipped) commands.push(words); words = []; skipped = false; };
  for (let i = 0; i < script.length; i++) {
    const c = script[i];
    if (quote === "'") {
      if (c === "'") quote = ''; else word += c;
      continue;
    }
    if (quote === '"') {
      if (c === '"') quote = '';
      else if (c === '\\' && i + 1 < script.length && '"\\$`'.includes(script[i + 1])) word += script[++i];
      else word += c;
      continue;
    }
    if (c === "'" || c === '"') { quote = c; inWord = true; continue; }
    if (c === '\\' && i + 1 < script.length) { word += script[++i]; inWord = true; continue; }
    if (c === '|' && script[i + 1] === '|') { endCommand(); skipped = true; i++; continue; }
    if (c === ';' || c === '&' || c === '|' || c === '\n') { endCommand(); continue; }
    if (c === ' ' || c === '\t' || c === '\r') { endWord(); continue; }
    word += c;
    inWord = true;
  }
  endCommand();
  return commands;
}

// splitCommand turns a provider's command (a string, or an argv array) into
// simple commands, unwrapping `bash -lc '<script>'` one level at a time.
function splitCommand(command, depth = 0) {
  let commands;
  if (Array.isArray(command)) {
    if (!command.every(part => typeof part === 'string')) return [];
    commands = [command];
  } else if (typeof command === 'string') {
    commands = tokenize(command);
  } else {
    return [];
  }
  const out = [];
  for (const words of commands) {
    const program = path.basename(words[0] || '');
    const flag = words.findIndex((word, i) => i > 0 && /^-[a-z]*c$/.test(word));
    if (shells.has(program) && flag > 0 && words[flag + 1] !== undefined && depth < 3) {
      out.push(...splitCommand(words[flag + 1], depth + 1));
    } else {
      out.push(words);
    }
  }
  return out;
}

function createReadTracker(root = process.cwd()) {
  const workspace = path.resolve(root);
  const opened = new Set();
  const diffed = new Set();
  let fullDiff = false;
  let commands = 0;

  // relative returns the repository-relative path of target, or '' when it
  // is outside the workspace or not a plausible path.
  function relative(target, cwd) {
    if (typeof target !== 'string' || target === '' || target.length > maxPath || /[\0-\x1f]/.test(target)) return '';
    const rel = path.relative(workspace, path.resolve(cwd, target));
    if (rel === '' || rel.startsWith('..') || path.isAbsolute(rel)) return '';
    return rel.split(path.sep).join('/');
  }
  function existing(target, cwd) {
    const rel = relative(target, cwd);
    if (!rel) return '';
    try { return fs.statSync(path.join(workspace, rel)).isFile() ? rel : ''; } catch { return ''; }
  }
  // blob reads `REV:path`; the path is relative to the repository root, and it
  // may name a file that no longer exists in the working tree.
  function blob(word) {
    const at = word.indexOf(':');
    if (at <= 0 || at === word.length - 1 || word.startsWith('-')) return '';
    let target = word.slice(at + 1);
    if (target.startsWith('./')) target = target.slice(2);
    return relative(target, workspace);
  }

  function git(words, cwd) {
    let i = 1;
    while (i < words.length && words[i].startsWith('-')) {
      if (words[i] === '-C') {
        if (words[i + 1] === undefined) return;
        cwd = path.resolve(cwd, words[i + 1]);
        i += 2;
      } else if (words[i] === '-c') {
        i += 2;
      } else {
        i++;
      }
    }
    const verb = words[i];
    const args = words.slice(i + 1);
    const dash = args.indexOf('--');
    const before = dash < 0 ? args : args.slice(0, dash);
    const pathspec = dash < 0 ? [] : args.slice(dash + 1);
    if (verb === 'show' || verb === 'cat-file') {
      for (const word of before) {
        const rel = blob(word);
        if (rel) opened.add(rel);
      }
      if (verb === 'show' && !before.some(word => noContent.test(word))) {
        for (const word of pathspec) { const rel = relative(word, cwd); if (rel) diffed.add(rel); }
      }
      return;
    }
    if (verb !== 'diff') return;
    if (before.some(word => noContent.test(word))) return;
    const operands = before.filter(word => !word.startsWith('-'));
    const named = operands.map(word => existing(word, cwd));
    const paths = [...pathspec.map(word => relative(word, cwd)), ...named].filter(Boolean);
    // Only a comparison against a revision shows the change: a bare
    // `git diff` is the working tree against the index, which on a committed
    // checkout is empty.
    const revisions = operands.filter((word, index) => !named[index]);
    if (paths.length === 0 && revisions.length > 0) fullDiff = true;
    for (const rel of paths) diffed.add(rel);
  }

  function simple(words, state) {
    while (words.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0])) words = words.slice(1);
    if (!words.length) return;
    const program = path.basename(words[0]);
    if (program === 'cd') {
      state.cwd = words[1] === undefined ? workspace : path.resolve(state.cwd, words[1]);
      return;
    }
    if (program === 'git') { git(words, state.cwd); return; }
    if (!readers.has(program)) return;
    for (const word of words.slice(1)) {
      if (word.startsWith('-')) continue;
      const rel = existing(word, state.cwd);
      if (rel) opened.add(rel);
    }
  }

  return {
    // command records one successfully executed shell command.
    command(command) {
      if (commands < maxCommands) commands++;
      let parts;
      try { parts = splitCommand(command); } catch { return; }
      const state = { cwd: workspace };
      for (const words of parts) {
        try { simple(words, state); } catch { /* an unparseable command reads nothing we can name */ }
      }
    },
    // readFile records one successful file read by a dedicated read tool.
    readFile(filePath) {
      if (commands < maxCommands) commands++;
      const rel = existing(filePath, workspace);
      if (rel) opened.add(rel);
    },
    record(source, method) {
      return { source, method, commands, full_diff: fullDiff, opened: [...opened].sort(), diffed: [...diffed].sort(), truncated: false };
    },
  };
}

// readsLine renders the record as the control line the worker strips, trimming
// path lists to the transport budget and saying so rather than failing.
function readsLine(record) {
  // The worker limits the line in bytes, so count bytes, not UTF-16 units.
  const bytes = text => Buffer.byteLength(text, 'utf8');
  const out = { ...record, opened: [], diffed: [] };
  let size = bytes(prefix) + bytes(JSON.stringify(out)) + 1;
  const queues = [['opened', record.opened || []], ['diffed', record.diffed || []]];
  for (const [key, paths] of queues) {
    for (const entry of paths) {
      const cost = bytes(JSON.stringify(entry)) + 1;
      if (size + cost > budget) { out.truncated = true; break; }
      out[key].push(entry);
      size += cost;
    }
  }
  return prefix + JSON.stringify(out) + '\n';
}

module.exports = { createReadTracker, readsLine, splitCommand };
