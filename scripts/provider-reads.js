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
// Diff options that print only some files or hunks, compare the index rather
// than the working tree, or write the patch to a file instead of the output.
// Such a diff did not show the change, so it earns no credit at all.
const filtered = /^(-[SG]|--(output|cached|staged|diff-filter|relative|find-object|skip-to|rotate-to|pickaxe-all|pickaxe-regex)(=|$))/;
const noContent = /^(-s|--(stat|numstat|shortstat|name-only|name-status|summary|dirstat|compact-summary|quiet|raw|no-patch)(=|$))/;

// tokenize splits a shell script into the simple commands a successful exit
// proves ran. It handles quotes, backslashes, and the operators ; & && || | and
// newlines. It is not a shell: expansions are kept literally, which only ever
// makes a path not match.
function tokenize(script) {
  const items = [];
  let words = [];
  let word = '';
  let inWord = false;
  let quote = '';
  // A word that began with an unquoted redirection operator. It is pushed
  // with the REDIRECT prefix, so a quoted literal `>` is never mistaken for
  // one, and so a redirection never resolves to a path.
  let redirect = false;
  const endWord = () => { if (inWord) words.push(redirect ? REDIRECT + word : word); word = ''; inWord = false; redirect = false; };
  const endCommand = sep => { endWord(); if (words.length) items.push({ words, sep }); words = []; };
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
    // `2>&1` and `<&3` are redirections, not background operators.
    if (c === '&' && (script[i - 1] === '>' || script[i - 1] === '<')) { word += c; inWord = true; continue; }
    // `&>` and `&>>` redirect stdout and stderr together.
    if (c === '&' && script[i + 1] === '>') { endWord(); word = c; inWord = true; redirect = true; continue; }
    // An unquoted redirection starts its own word even when attached
    // (`file.ts>/tmp/out`), keeping a file-descriptor prefix (`2>`, `&>`) and
    // a doubled operator (`>>`) with it. The target that follows joins it.
    if (c === '>' || c === '<') {
      const prefix = /^(\d+|&)$/.test(word) || (redirect && /^(\d+|&)?[<>]+$/.test(word));
      if (!prefix) endWord();
      word += c;
      inWord = true;
      redirect = true;
      continue;
    }
    if ((c === '&' || c === '|') && script[i + 1] === c) { endCommand(c + c); i++; continue; }
    if (c === ';' || c === '&' || c === '|' || c === '\n') { endCommand(c); continue; }
    if (c === ' ' || c === '\t' || c === '\r') { endWord(); continue; }
    word += c;
    inWord = true;
  }
  endCommand(null);
  return certain(items);
}

// certain keeps the commands that must have run for the whole script to exit
// successfully, and whose output then reached the reviewer whole. A script's status is its last statement's, so every command of
// a last statement that is a plain && chain ran. Anywhere else only the first
// unit is certain: in an earlier statement a failure skips the rest of its &&
// chain and the script still succeeds, and in `a && b || c` a failing `a`
// skips `b` while `c` makes the whole succeed. Pipelines run as one unit.
function certain(items) {
  const statements = [];
  let current = [];
  for (const item of items) {
    current.push(item);
    if (item.sep === ';' || item.sep === '&' || item.sep === '\n' || item.sep === null) {
      statements.push(current);
      current = [];
    }
  }
  if (current.length) statements.push(current);
  const out = [];
  statements.forEach((statement, index) => {
    const firstOnly = index !== statements.length - 1 || statement.some(item => item.sep === '||');
    statement.forEach((item, position) => {
      if (stopped(statement, position, firstOnly)) return;
      // cd changes where later relative paths resolve whatever happened to
      // its output, so it is kept for its effect; it earns no credit itself.
      if (visible(statement, position) || path.basename(item.words[0] || '') === 'cd') out.push(item.words);
    });
  });
  return out;
}

// stopped reports whether an earlier && or || in a first-unit-only statement
// cut the statement off before this command.
function stopped(statement, position, firstOnly) {
  return firstOnly && statement.slice(0, position).some(item => item.sep === '&&' || item.sep === '||');
}

// Commands that hand their input on unchanged. Anything else in a pipeline --
// wc, head, grep, sed -n -- summarises or truncates what it is given (#98).
const passthrough = new Set(['cat', 'less', 'more', 'tee']);

// REDIRECT marks a word the tokenizer read as an unquoted redirection. It is a
// control character, which no credited path may contain.
const REDIRECT = '\u0001';

// redirection returns the redirection a word carries ("" for an ordinary
// argument), without its marker.
function redirection(word) {
  return word.startsWith(REDIRECT) ? word.slice(REDIRECT.length) : '';
}

// withoutRedirections drops redirections and their targets, which are not
// arguments: `2>&1` and `2> err.log` are not git revisions or file reads.
function withoutRedirections(words) {
  const out = [];
  for (let i = 0; i < words.length; i++) {
    const r = redirection(words[i]);
    if (!r) { out.push(words[i]); continue; }
    // A bare operator (`2>`, `>>`, `<`, `2>&`) takes the next word as its target.
    if (/^(\d+|&)?([<>]+|[<>]&)$/.test(r)) i++;
  }
  return out;
}

// redirectsStdout reports whether a command's standard output ends anywhere
// but the terminal the reviewer reads. Redirections apply left to right, as
// in a shell, so it tracks where descriptors 1 and 2 point (#104):
// `2>/dev/null 1>&2` discards stdout, while `1>&2 2>/dev/null` keeps it on
// the terminal. A file, a closed descriptor, or one not otherwise known
// (`1>&3`) all count as not visible.
function redirectsStdout(words) {
  const fds = { 1: 'tty', 2: 'tty' };
  for (let i = 0; i < words.length; i++) {
    const r = redirection(words[i]);
    if (!r) continue;
    const m = /^(\d+|&)?(>&|>>?|<.*)(.*)$/.exec(r);
    if (!m || m[2].startsWith('<')) continue;
    let target = m[3];
    // A bare operator (`2>`, `>`, `2>&`) takes the next word as its target.
    if (target === '') target = redirection(words[i + 1] || '') || words[++i] || '';
    let dest;
    if (m[2] === '>&' && /^\d+$/.test(target)) dest = fds[target] || 'other';
    else if (m[2] === '>&' && target === '-') dest = 'closed';
    else dest = 'file';
    // `&>file` and bash's `>&file` send both stdout and stderr to the file.
    if (m[1] === '&' || (m[2] === '>&' && dest === 'file' && !m[1])) { fds[1] = dest; fds[2] = dest; continue; }
    fds[m[1] || '1'] = dest;
  }
  return fds[1] !== 'tty';
}

// passesStdin reports whether a pass-through command actually forwards the
// pipe. `cat FILE` and `less < FILE` read something else and drop it; `-` is
// stdin, and options such as `cat -n` keep reading it. tee always forwards
// stdin, whatever files it also writes.
function passesStdin(words) {
  if (!passthrough.has(path.basename(words[0] || ''))) return false;
  if (words.some(word => /^\d*</.test(redirection(word)))) return false;
  // --help and --version print their own text and exit without reading stdin.
  if (words.some(word => /^--(help|version)$/.test(word))) return false;
  if (path.basename(words[0]) === 'tee') return true;
  return words.slice(1).every(word => word === '-' || (word.startsWith('-') && word.length > 1) || redirection(word) !== '');
}

// visible reports whether a command's output reached the reviewer whole: it
// is not redirected to a file, and every command it is piped into passes the
// output through unchanged. A diff piped into `wc -l` was run, but only a
// count was seen.
function visible(statement, position) {
  if (redirectsStdout(statement[position].words)) return false;
  for (let next = position + 1; next < statement.length && statement[next - 1].sep === '|'; next++) {
    const words = statement[next].words;
    if (!passesStdin(words) || redirectsStdout(words)) return false;
  }
  return true;
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
  // range -> paths diffed by that comparison
  const diffed = new Map();
  let fullDiff = false;
  // The revision operands of every whole-diff command. The worker checks
  // them against the merge base; `git diff HEAD` is a revision diff too.
  const ranges = [];
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
      // `REV:path` prints the file. `git show REV -- path` prints one commit's
      // change to it, which on a longer branch is not the change under review.
      for (const word of before) {
        const rel = blob(word);
        if (rel) opened.add(rel);
      }
      return;
    }
    if (verb !== 'diff') return;
    if (before.some(word => noContent.test(word) || filtered.test(word))) return;
    const operands = before.filter(word => !word.startsWith('-'));
    const named = operands.map(word => existing(word, cwd));
    const paths = [...pathspec.map(word => relative(word, cwd)), ...named].filter(Boolean);
    // Only a comparison against a revision shows the change: a bare
    // `git diff` is the working tree against the index, which on a committed
    // checkout is empty. The worker checks each range against the merge base.
    const revisions = operands.filter((word, index) => !named[index]);
    if (revisions.length === 0) return;
    const range = revisions.join(' ');
    if (range.length > 200) return;
    if (paths.length === 0) {
      fullDiff = true;
      if (ranges.length < 20 && !ranges.includes(range)) ranges.push(range);
      return;
    }
    if (!diffed.has(range)) {
      if (diffed.size >= 50) return;
      diffed.set(range, new Set());
    }
    for (const rel of paths) diffed.get(range).add(rel);
  }

  function simple(words, state) {
    words = withoutRedirections(words);
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
      return { source, method, commands, full_diff: fullDiff, diff_ranges: [...ranges], opened: [...opened].sort(),
        diffed: [...diffed].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([range, paths]) => ({ range, paths: [...paths].sort() })),
        truncated: false };
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
  const fits = cost => {
    if (size + cost > budget) { out.truncated = true; return false; }
    size += cost;
    return true;
  };
  for (const entry of record.opened || []) {
    if (!fits(bytes(JSON.stringify(entry)) + 1)) break;
    out.opened.push(entry);
  }
  for (const { range, paths } of record.diffed || []) {
    const kept = { range, paths: [] };
    if (!fits(bytes(JSON.stringify(kept)) + 1)) break;
    out.diffed.push(kept);
    for (const entry of paths) {
      if (!fits(bytes(JSON.stringify(entry)) + 1)) break;
      kept.paths.push(entry);
    }
  }
  return prefix + JSON.stringify(out) + '\n';
}

module.exports = { createReadTracker, readsLine, splitCommand };
