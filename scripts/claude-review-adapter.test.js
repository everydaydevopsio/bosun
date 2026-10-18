'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createCollector, denialSummary } = require('./claude-review-adapter');

test('keeps final review and reports denied Git command without arguments', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text: 'private prompt' }] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete', permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git merge-base secret-value HEAD' } },
  ] }));
  assert.match(collector.output(), /BOSUN-REVIEW: incomplete/);
  assert.match(collector.output(), /git merge-base \(1\)/);
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
  assert.match(collector.output(), /git diff \(1\)/);
});
