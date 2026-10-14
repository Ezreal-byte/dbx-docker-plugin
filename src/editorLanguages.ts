import { yaml } from '@codemirror/lang-yaml';
import { json } from '@codemirror/lang-json';
import { xml } from '@codemirror/lang-xml';
import { StreamLanguage } from '@codemirror/language';
import { properties } from '@codemirror/legacy-modes/mode/properties';
import { toml } from '@codemirror/legacy-modes/mode/toml';
import { nginx } from '@codemirror/legacy-modes/mode/nginx';
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';

export function languageName(path: string): string {
  const name = path.split('/').pop()?.toLowerCase() || '';
  if (/\.(ya?ml)$/.test(name)) return 'YAML';
  if (/\.(json|jsonc)$/.test(name)) return 'JSON';
  if (/\.(xml|svg|html|xhtml)$/.test(name)) return 'XML';
  if (/\.toml$/.test(name)) return 'TOML';
  if (name === 'dockerfile' || name.startsWith('dockerfile.')) return 'Dockerfile';
  if (/nginx|\.nginx$/.test(name)) return 'Nginx';
  if (/\.(sh|bash|zsh)$/.test(name) || /^\.(bashrc|profile|zshrc)$/.test(name)) return 'Shell';
  if (/\.(properties|ini|cfg|conf|env)$/.test(name) || /^\.env($|\.)/.test(name)) return 'Properties / INI';
  return 'Text';
}
export function languageExtension(path: string) {
  switch (languageName(path)) {
    case 'YAML': return yaml();
    case 'JSON': return json();
    case 'XML': return xml();
    case 'TOML': return StreamLanguage.define(toml);
    case 'Dockerfile': return StreamLanguage.define(dockerFile);
    case 'Nginx': return StreamLanguage.define(nginx);
    case 'Shell': return StreamLanguage.define(shell);
    case 'Properties / INI': return StreamLanguage.define(properties);
    default: return [];
  }
}
