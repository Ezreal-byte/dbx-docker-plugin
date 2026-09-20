import { describe, expect, it } from 'vitest';
import { directoryOf, savedPathKind } from './format';

describe('saved download paths', () => {
  it('takes the directory from a Windows absolute path', () => {
    expect(directoryOf('C:\\Users\\me\\Downloads\\nginx-latest.tar')).toBe('C:\\Users\\me\\Downloads');
  });

  it('takes the directory from a POSIX absolute path', () => {
    expect(directoryOf('/home/me/exports/nginx-latest.tar')).toBe('/home/me/exports');
  });

  it('ignores trailing separators', () => {
    expect(directoryOf('/home/me/exports/')).toBe('/home/me/exports');
    expect(directoryOf('C:\\Users\\me\\Downloads\\')).toBe('C:\\Users\\me\\Downloads');
  });

  it('returns the input unchanged when there is no directory part', () => {
    expect(directoryOf('nginx-latest.tar')).toBe('nginx-latest.tar');
  });

  it('classifies a bare file name as a browser download', () => {
    expect(savedPathKind('nginx-latest.tar', 'nginx-latest.tar')).toBe('web');
  });

  it('classifies an absolute path as a desktop save', () => {
    expect(savedPathKind('C:\\Users\\me\\Downloads\\nginx-latest.tar', 'nginx-latest.tar')).toBe('desktop');
    expect(savedPathKind('/home/me/exports/nginx-latest.tar', 'nginx-latest.tar')).toBe('desktop');
  });

  it('treats any path containing a separator as a desktop save', () => {
    expect(savedPathKind('downloads/nginx-latest.tar', 'nginx-latest.tar')).toBe('desktop');
  });
});
