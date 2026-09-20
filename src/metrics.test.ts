import { describe, expect, it } from 'vitest';
import { computeRate, formatRate, timeLabels } from './metrics';

function sample(seconds: number, value: number) {
  return { readAt: new Date(Date.UTC(2026, 0, 1, 0, 0, seconds)).toISOString(), value };
}

describe('counter rate conversion', () => {
  it('returns 0 for the first sample because there is no previous value', () => {
    expect(computeRate([sample(0, 1000)])).toEqual([0]);
  });

  it('converts a byte delta into bytes per second', () => {
    const rates = computeRate([sample(0, 0), sample(2, 4096)]);
    expect(rates[1]).toBeCloseTo(2048, 5);
  });

  it('returns 0 when the counters did not move', () => {
    expect(computeRate([sample(0, 500), sample(4, 500)])).toEqual([0, 0]);
  });

  it('clamps counter resets (container restart) to 0 instead of a negative rate', () => {
    const rates = computeRate([sample(0, 9000), sample(2, 10)]);
    expect(rates[1]).toBe(0);
  });

  it('returns 0 when the timestamps do not advance', () => {
    const rates = computeRate([sample(3, 100), sample(3, 900)]);
    expect(rates[1]).toBe(0);
  });

  it('survives unparseable timestamps', () => {
    const rates = computeRate([{ readAt: 'nope', value: 1 }, { readAt: 'nope', value: 9 }]);
    expect(rates).toEqual([0, 0]);
  });

  it('handles an empty series', () => {
    expect(computeRate([])).toEqual([]);
  });
});

describe('chart labels', () => {
  it('maps timestamps to local time labels', () => {
    const labels = timeLabels([new Date(Date.UTC(2026, 0, 1, 12, 0, 0)).toISOString()]);
    expect(labels).toHaveLength(1);
    expect(labels[0]).not.toBe('');
  });

  it('emits an empty label for an invalid timestamp', () => {
    expect(timeLabels(['nope'])).toEqual(['']);
  });
});

describe('rate formatting', () => {
  it('formats bytes per second with a unit suffix', () => {
    expect(formatRate(0)).toBe('0 B/s');
    expect(formatRate(2048)).toBe('2.0 KB/s');
    expect(formatRate(1024 * 1024 * 5)).toBe('5.0 MB/s');
  });

  it('never reports a negative rate', () => {
    expect(formatRate(-100)).toBe('0 B/s');
  });
});
