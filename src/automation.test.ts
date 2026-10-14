import { describe, expect, it } from 'vitest';
import { recommendations } from './automation';

describe('AI recommendations', () => {
  it('only sends the selected resource identifiers', () => {
    const result = recommendations('volumes', 'test-volume', true);
    expect(result.context).toEqual({ resource: { kind: 'volumes', id: 'test-volume' } });
    expect(result.items).toHaveLength(1);
    expect(result.items[0].prompt).toContain('test-volume');
    expect(result.items[0].label).toContain('已停止');
  });
  it('changes the question with the resource and language', () => {
    expect(recommendations('containers', 'container-id', false).items[0].label).toContain('exit causes');
    expect(recommendations('networks', 'network-id', false).items[0].label).toContain('endpoints');
    expect(recommendations('images', 'image-id', true).items[0].label).toContain('摘要');
  });
});
