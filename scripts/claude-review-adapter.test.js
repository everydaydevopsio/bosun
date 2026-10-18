'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createCollector, denialSummary, stderrKind } = require('./claude-review-adapter');

test('keeps final review and reports denied Git command without arguments', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text: 'private prompt' }] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete', permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git merge-base secret-value HEAD' } },
  ] }));
  assert.match(collector.output(), /BOSUN-REVIEW: incomplete/);
  assert.match(collector.output(), /git merge-base <arg> HEAD \(1\)/);
  assert.match(collector.output(), /^Claude denied/);
  assert.doesNotMatch(collector.output(), /secret-value|private prompt/);
  assert.equal(collector.failed(), false);
});

test('empty or failed stream is an error', () => {
  const collector = createCollector();
  collector.accept('{invalid');
  assert.equal(collector.failed(), true);
  assert.equal(denialSummary([]), '');
});

test('reports a denied tool result when older Claude omits result permission_denials', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [
    { type: 'tool_use', id: 'call-1', name: 'Bash', input: { command: 'git diff --stat HEAD' } },
  ] } }));
  collector.accept(JSON.stringify({ type: 'user', message: { content: [
    { type: 'tool_result', tool_use_id: 'call-1', is_error: true, content: 'Permission denied' },
  ] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete' }));
  assert.match(collector.output(), /git diff --stat HEAD \(1\)/);
});

test('masks paths and notes command forms that cannot match a narrow Git rule', () => {
  const summary = denialSummary([{ tool_name: 'Bash', tool_input: {
    command: 'git -C /repos/review.secret rev-parse HEAD && cat /private/token',
  } }]);
  assert.match(summary, /git -C <dir> rev-parse HEAD <arg> <arg> <arg> \[compound or redirected\]/);
  assert.doesNotMatch(summary, /review.secret|private\/token/);
});

test('keeps complete review clean even if a tool call was denied', () => {
  const collector = createCollector();
  const review = 'Reviewed HEAD\nBOSUN-REVIEW: complete';
  collector.accept(JSON.stringify({ type: 'result', result: review, permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git branch -D main' } },
  ] }));
  assert.equal(collector.output(), review);
});

test('includes denial summary for lowercase, indented incomplete trailer', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'result', result: 'Could not read diff\n  bosun-review: incomplete', permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git merge-base HEAD main' } },
  ] }));
  assert.match(collector.output(), /^Claude denied 1 tool call/);
});

test('labels fallback tool errors without claiming Claude denied them', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [
    { type: 'tool_use', id: 'call-1', name: 'Read', input: { file_path: '/private/data' } },
  ] } }));
  collector.accept(JSON.stringify({ type: 'user', message: { content: [
    { type: 'tool_result', tool_use_id: 'call-1', is_error: true, content: 'EACCES permission denied' },
  ] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete' }));
  assert.match(collector.output(), /^1 tool error\(s\) mentioned permissions: Read/);
  assert.doesNotMatch(collector.output(), /Git allowlist/);
});

test('classifies stderr without exposing its text', () => {
  assert.equal(stderrKind('Unknown option --secret=private'), 'CLI_FLAG_ERROR');
  assert.equal(stderrKind('opaque secret data'), 'OTHER_STDERR_ERROR');
});
