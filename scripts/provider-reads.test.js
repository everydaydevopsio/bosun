'use strict';
// #93: read evidence is a lower bound on what the reviewer examined. It names
// paths and never carries file content.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { createReadTracker, readsLine, splitCommand } = require('./provider-reads');

function workspace() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'bosun-reads-'));
  for (const file of ['src/app.ts', 'src/util.ts', 'package.json', 'README.md']) {
    fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    fs.writeFileSync(path.join(root, file), 'secret source content\n');
  }
  return root;
}

test('splitCommand unwraps a shell -c script and splits compound commands', () => {
  assert.deepEqual(splitCommand(`/bin/bash -lc "sed -n '1,40p' src/app.ts && cat package.json | head"`), [
    ['sed', '-n', '1,40p', 'src/app.ts'], ['cat', 'package.json'], ['head'],
  ]);
  assert.deepEqual(splitCommand(['bash', '-lc', 'git diff base..HEAD; nl -ba README.md']), [
    ['git', 'diff', 'base..HEAD'], ['nl', '-ba', 'README.md'],
  ]);
});

test('file readers record existing workspace files only', () => {
  const root = workspace();
  const tracker = createReadTracker(root);
  tracker.command(`bash -lc "sed -n '1,200p' src/app.ts"`);
  tracker.command('cat package.json missing.ts /etc/passwd ../outside.ts');
  tracker.command('rg -n useState src/util.ts'); // a search is not a read
  const record = tracker.record('codex', 'command_parsing');
  assert.deepEqual(record.opened, ['package.json', 'src/app.ts']);
  assert.equal(record.commands, 3);
  assert.equal(record.full_diff, false);
});

test('git commands distinguish a full diff, a path diff, a stat, and a blob read', () => {
  const root = workspace();
  const tracker = createReadTracker(root);
  tracker.command('git diff --stat abc..HEAD');
  let record = tracker.record('codex', 'command_parsing');
  assert.equal(record.full_diff, false, 'a stat shows no content');
  tracker.command('git --no-pager diff abc...HEAD -- src/util.ts');
  tracker.command('git show HEAD:README.md');
  tracker.command('git cat-file -p HEAD:src/deleted.ts');
  record = tracker.record('codex', 'command_parsing');
  assert.deepEqual(record.diffed, ['src/util.ts']);
  assert.deepEqual(record.opened, ['README.md', 'src/deleted.ts']);
  assert.equal(record.full_diff, false);
  tracker.command('git diff abc...HEAD');
  assert.equal(tracker.record('codex', 'command_parsing').full_diff, true);
});

test('cd inside a script resolves later relative paths', () => {
  const root = workspace();
  const tracker = createReadTracker(root);
  tracker.command('bash -lc "cd src && cat app.ts"');
  assert.deepEqual(tracker.record('codex', 'command_parsing').opened, ['src/app.ts']);
});

test('the Read tool records an absolute path relative to the workspace', () => {
  const root = workspace();
  const tracker = createReadTracker(root);
  tracker.readFile(path.join(root, 'src/app.ts'));
  tracker.readFile('/etc/hosts');
  tracker.readFile(42);
  assert.deepEqual(tracker.record('claude', 'tool_calls').opened, ['src/app.ts']);
});

test('the reads line is bounded and never carries content', () => {
  const root = workspace();
  const tracker = createReadTracker(root);
  for (let i = 0; i < 3000; i++) tracker.command(`git show HEAD:dir/${'x'.repeat(100)}-${i}.ts`);
  tracker.command('cat src/app.ts');
  const line = readsLine(tracker.record('codex', 'command_parsing'));
  assert.ok(line.startsWith('BOSUN_READS_V1 {'));
  assert.ok(line.endsWith('\n'));
  assert.ok(line.length < 60 * 1024, `line is ${line.length} bytes`);
  const parsed = JSON.parse(line.slice('BOSUN_READS_V1 '.length));
  assert.equal(parsed.truncated, true);
  assert.equal(parsed.source, 'codex');
  assert.doesNotMatch(line, /secret source content/);
});

test('malformed input never throws', () => {
  const tracker = createReadTracker(workspace());
  for (const input of [undefined, null, 7, {}, '', 'echo "unterminated', "sed -n '1p", 'git', 'git -C']) {
    tracker.command(input);
  }
  assert.equal(tracker.record('codex', 'command_parsing').opened.length, 0);
});
