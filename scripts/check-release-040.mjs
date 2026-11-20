// Isolated live regression for issues #3/#4; no existing project is modified.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { Sidecar } from './sidecar-client.mjs';

const root = await mkdtemp(join(tmpdir(), 'dbx-release040-'));
const sourceDir = join(root, 'source'), dataDir = join(root, 'data');
await mkdir(sourceDir); await mkdir(dataDir);
const file = join(sourceDir, 'compose.yaml'), envFile = join(sourceDir, 'custom.env');
const project = 'dbx040-' + randomUUID().slice(0, 8), cloneName = project + '-copy';
const image = process.argv[3] || 'alpine:3.22';
const dockerHost = process.platform === 'win32' ? 'npipe:////./pipe/docker_engine' : 'unix:///var/run/docker.sock';
const docker = (...args) => execFileSync('docker', ['--host', dockerHost, ...args], {encoding:'utf8',timeout:120000,stdio:['pipe','pipe','pipe']}).trim();
const composeArgs = ['compose','--project-name',project,'--project-directory',sourceDir,'--env-file',envFile,'--file',file];
const content = `# keep this comment 原始配置\nservices:\n  first:\n    image: \${APP_IMAGE}\n    command: [sleep, '360']\n    volumes: [data:/data]\n  second:\n    image: \${APP_IMAGE}\n    command: [sleep, '360']\n    depends_on: [first]\nvolumes:\n  data: {}\n`;
await writeFile(file,content); await writeFile(envFile,`APP_IMAGE=${image}\nUNUSED=one\n`);
const client = new Sidecar(resolve(process.argv[2]),dataDir);
const connection = { id:project,plugin_id:'io.dbx.docker',host:'127.0.0.1',external_config:{protocol:'local'} };
const rpc = (method,args={})=>client.request(method,{connectionId:project,...args});
const preview = (operation,args)=>rpc('docker/mutation',{operation,arguments:args});
const execute = (operation,args,token)=>preview(operation,{...args,previewToken:token});
async function waitTask(id) {
  const deadline=Date.now()+120000;
  while(Date.now()<deadline){const task=await rpc('docker/taskStatus',{taskId:id});if(task.status!=='running'){assert.equal(task.status,'completed',task.error);return task;}await new Promise(r=>setTimeout(r,200));}
  throw Error('task did not complete');
}
async function action(action,extra={}){
  const args={project,action,...extra},plan=await preview('compose_project',args);
  assert.equal(plan.executed,false); assert(!JSON.stringify(plan).includes('APP_IMAGE'));
  const result=await execute('compose_project',args,plan.previewToken);await waitTask(result.taskId);
  await assert.rejects(execute('compose_project',args,plan.previewToken),/token/i);
}
try {
  docker(...composeArgs,'up','--detach');
  await rpc('connection/connect',{connection});
  await assert.rejects(rpc('docker/composeProjectAction',{project,action:'down'}),/preview/i);
  const projects=await rpc('docker/composeProjects');assert(projects.some(p=>p.name===project&&p.containers===2));
  let source=await rpc('docker/composeSource',{project});assert.equal(source.files[0].content,content);assert.equal(source.editable,true);
  const args={project,action:'deploy',content:content.replace("'360'","'480'"),sourceHash:source.sourceHash};
  const plan=await preview('compose_project',args);assert.equal(await readFile(file,'utf8'),content);
  assert.equal(plan.preview.services.length,2);assert.equal(plan.preview.containers.length,2);
  assert(!JSON.stringify(plan).includes('UNUSED='),'review must not expose environment contents');
  await writeFile(file,content+'# changed outside plugin\n');
  await assert.rejects(execute('compose_project',args,plan.previewToken),/source changed|state changed|target.*changed/i);
  await writeFile(file,content);
  const envPlan=await preview('compose_project',{project,action:'deploy'});
  await writeFile(envFile,'APP_IMAGE=alpine:dbx-test-not-an-image\n');
  await assert.rejects(execute('compose_project',{project,action:'deploy'},envPlan.previewToken),/target.*changed|state changed/i);
  await writeFile(envFile,`APP_IMAGE=${image}\n`);
  console.log('PASS: original source and stale YAML/.env preview rejection');
  await action('deploy',args);
  assert.equal(await readFile(file,'utf8'),args.content);assert((await readFile(file,'utf8')).startsWith('# keep this comment'));
  const id=docker(...composeArgs,'ps','-q','first');
  await assert.rejects(rpc('docker/cloneContainer',{containerId:id,name:cloneName}),/preview/i);
  const cloneArgs={containerId:id,name:cloneName};const copyPlan=await preview('clone_container',cloneArgs);
  const copy=await execute('clone_container',cloneArgs,copyPlan.previewToken);
  const inspected=JSON.parse(docker('inspect',copy.id))[0];assert.equal(inspected.State.Running,false);assert.equal(inspected.Mounts.length,0);assert(!inspected.Config.Labels?.['com.docker.compose.project']);
  await assert.rejects(execute('clone_container',cloneArgs,copyPlan.previewToken),/token/i);
  const readonly={...connection,read_only:true};await rpc('connection/connect',{connection:readonly});
  await assert.rejects(preview('clone_container',cloneArgs),/read.only/i);await assert.rejects(preview('compose_project',{project,action:'stop'}),/read.only/i);
  await rpc('connection/connect',{connection});
  await action('stop');assert.equal(docker(...composeArgs,'ps','--status','running','-q'),'');
  await action('start');await action('restart');
  const volumes=docker('volume','ls','--filter',`label=com.docker.compose.project=${project}`,'-q');assert(volumes);
  await action('down');for(const volume of volumes.split('\n'))docker('volume','inspect',volume);
  assert((await rpc('docker/composeProjects')).some(p=>p.name===project&&p.containers===0));
  await action('deploy');assert(docker(...composeArgs,'ps','-q'));
  console.log('PASS: original YAML/comments/.env/dependencies, stale-source denial, atomic edit/deploy, start/stop/restart/down/redeploy, retained volumes, stopped isolated copy, preview replay and read-only gates');
} finally {
  await client.close();
  try {docker('rm','-f',cloneName);}catch{}
  try {docker(...composeArgs,'down','--volumes');}catch(e){console.error('Test project cleanup failed:',project,e.message);}
  if(!resolve(root).startsWith(resolve(tmpdir())+sep))throw Error('Refusing unscoped cleanup');
  await rm(root,{recursive:true,force:true});
}
