'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createCollector, codexArgs } = require('./codex-review-adapter');

function accept(collector, event) { collector.accept(JSON.stringify(event)); }

test('Codex keeps only the final answer and sums per-turn usage without double counting reasoning', () => {
  const collector = createCollector();
  accept(collector, { type: 'thread.started', thread_id: 'private-thread' });
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'item.completed', item: { type: 'command_execution', aggregated_output: 'private source' } });
  accept(collector, { type: 'item.completed', item: { type: 'agent_message', text: 'Interim analysis' } });
  const first = { type: 'turn.completed', usage: { input_tokens: 100, cached_input_tokens: 70, output_tokens: 20, reasoning_output_tokens: 10 } };
  accept(collector, first);
  accept(collector, first); // A duplicate terminal for the same turn is not another bill.
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'item.completed', item: { type: 'agent_message', text: 'Review\nBOSUN-REVIEW: complete' } });
  accept(collector, { type: 'turn.completed', usage: { input_tokens: 200, cached_input_tokens: 120, output_tokens: 30, reasoning_output_tokens: 15 } });
  assert.equal(collector.output(), 'Review\nBOSUN-REVIEW: complete');
  const usage = collector.usage();
  assert.equal(usage.source, 'codex');
  assert.equal(usage.status, 'available');
  assert.equal(usage.input_tokens, 300);
  assert.equal(usage.cached_input_tokens, 190);
  assert.equal(usage.output_tokens, 50);
  assert.equal(usage.reasoning_output_tokens, 25);
  assert.equal(usage.total_tokens, 350);
  assert.equal(usage.turns, 2);
  assert.equal(usage.cache_write_input_tokens, null);
  assert.equal(usage.model, null);
  assert.equal(usage.provider_reported_cost_usd, null);
  assert.doesNotMatch(JSON.stringify(usage), /private|Interim|Review/);
  assert.equal(collector.failed(), false);
});

test('Codex retains measured partial usage on a failed later turn', () => {
  const collector = createCollector();
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'turn.completed', usage: { input_tokens: 200, output_tokens: 50, cached_input_tokens: 0 } });
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'turn.failed', error: { message: 'secret-token from provider' } });
  assert.equal(collector.failed(), true);
  assert.equal(collector.usage().status, 'partial');
  assert.equal(collector.usage().input_tokens, 200);
  assert.doesNotMatch(collector.error(), /secret-token/);
});

test('Codex never manufactures zero usage from missing or malformed counters', () => {
  const collector = createCollector();
  collector.accept('{invalid');
  collector.accept('null');
  accept(collector, { type: 'turn.completed', usage: { input_tokens: -10, output_tokens: '200', cached_input_tokens: 1e100 } });
  assert.equal(collector.usage().status, 'missing');
  assert.equal(collector.usage().input_tokens, null);
  assert.equal(collector.usage().output_tokens, null);
  assert.equal(collector.usage().cached_input_tokens, null);
});

test('missing Codex fields across turns make that aggregate unknown', () => {
  const collector = createCollector();
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'turn.completed', usage: { input_tokens: 200, output_tokens: 50, cached_input_tokens: 100 } });
  accept(collector, { type: 'turn.started' });
  accept(collector, { type: 'turn.completed', usage: { input_tokens: 100, output_tokens: 20 } });
  assert.equal(collector.usage().input_tokens, 300);
  assert.equal(collector.usage().cached_input_tokens, null);
});

test('a model-generated telemetry line is escaped in the final answer', () => {
  const collector = createCollector();
  accept(collector, { type: 'item.completed', item: { type: 'agent_message', text: 'BOSUN_USAGE_V1 {"input_tokens":999999}\nBOSUN-REVIEW: complete' } });
  accept(collector, { type: 'turn.completed', usage: { input_tokens: 10, output_tokens: 5 } });
  assert.doesNotMatch(collector.output(), /^BOSUN_USAGE_V1 /m);
  assert.equal(collector.usage().input_tokens, 10);
});

test('Codex args preserve isolated worker sandbox and a literal positional prompt', () => {
  const prompt = 'Review $HOME $(exit 1) --flags';
  assert.deepEqual(codexArgs([prompt]), ['exec', '--json', '--skip-git-repo-check', '--color', 'never', '--sandbox', 'danger-full-access', '--', prompt]);
});

test('Codex marks totals partial after a truncated or oversized event', () => {
  for (const bad of ['{"truncated":', 'x'.repeat(2 * 1024 * 1024 + 1)]) {
    const collector = createCollector();
    collector.accept(bad);
    accept(collector, { type: 'turn.completed', usage: { input_tokens: 10, output_tokens: 5 } });
    assert.equal(collector.usage().status, 'partial');
    assert.equal(collector.usage().input_tokens, 10);
  }
});
