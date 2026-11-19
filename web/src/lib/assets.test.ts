import { test } from 'node:test'
import assert from 'node:assert/strict'
import { assetName, dirCount, dirTree, inDir, ROOT_DIR, subtypeOf, variantCount, wallSections, batchesOf, metaRows, variantNames, viewGroups, canReview, candidateLabel, comparePair, pendingStatus, recipientOf, stepQueue, coverOf, wallCover, needsAutoThumb, reviewProgress, facetsOf, fileTypeOf, fileTypesOf, fileTagsOf, mediaKindOf, uniqTags, defaultView, detailViews, splitWidth, splitWidthCss, SPLIT_MIN, filterAssets, fmtSize, fmtTris, glbOf, groupMembers, groupStatus, naturalSort, nextInQueue, overBudget, projectsOf, reviewQueue, wallTiles, type AssetLite, type FileLite } from './assets.ts'

const png = (name: string): FileLite => ({ id: name, name, mime: 'image/png', size: 1 })
const glb = (name: string): FileLite => ({ id: name, name, mime: 'model/gltf-binary', size: 1 })
let seq = 0
const mk = (x: Partial<AssetLite> & { key: string }): AssetLite => ({
  id: `id${++seq}`,
  code: `A-${seq}`,
  project_id: 'p1',
  batch: 'G11e1',
  kind: 'part',
  category: '车轮',
  status: 'pending',
  version: 1,
  updated_at: '2026-11-19T00:00:00Z',
  ...x,
})

test('项目按最近更新排，同一项目只出现一次', () => {
  const list = [mk({ key: 'a', project_id: 'p1', updated_at: '2026-11-18T00:00:00Z' }), mk({ key: 'b', project_id: 'p2', updated_at: '2026-11-19T00:00:00Z' }), mk({ key: 'c', project_id: 'p1', updated_at: '2026-11-17T00:00:00Z' })]
  assert.deepEqual(projectsOf(list), ['p2', 'p1'])
})

test('批次按自然顺序：G2 在 G11 前，G11e2 在 G11e10 前', () => {
  assert.deepEqual(naturalSort(['G11e10', 'G2', 'G11e2', 'G11d']), ['G2', 'G11d', 'G11e2', 'G11e10'])
})

test('分面只看当前项目，且不受筛选影响；各状态都有计数', () => {
  const list = [mk({ key: 'a', batch: 'G11e2', category: '座椅' }), mk({ key: 'b', batch: 'G11e1', status: 'approved' }), mk({ key: 'c', project_id: 'p2', batch: 'X' })]
  const f = facetsOf(list, 'p1')
  assert.deepEqual(f.batches, ['G11e1', 'G11e2'])
  assert.deepEqual(f.categories, ['车轮', '座椅'].sort(new Intl.Collator('zh', { numeric: true }).compare))
  assert.deepEqual(f.counts, { wip: 0, pending: 1, approved: 1, rework: 0 })
  assert.equal(f.total, 2)
})

test('筛选：项目必选，批次 / 类别 / 状态空串表示全部', () => {
  const list = [mk({ key: 'a', batch: 'G11e2', category: '座椅' }), mk({ key: 'b', status: 'approved' }), mk({ key: 'c', project_id: 'p2' })]
  assert.equal(filterAssets(list, { project: 'p1', batch: '', category: '', status: '' }).length, 2)
  assert.deepEqual(filterAssets(list, { project: 'p1', batch: 'G11e2', category: '', status: '' }).map((a) => a.key), ['a'])
  assert.deepEqual(filterAssets(list, { project: 'p1', batch: '', category: '', status: 'approved' }).map((a) => a.key), ['b'])
})

test('图片墙：同批次同组的候选收成一格，放在第一张的位置；不同批次同名组不合并；落单的照常一格', () => {
  const list = [
    mk({ key: 'cat', kind: 'image', batch: 'G11d' }),
    mk({ key: 'utility_4x4_B', kind: 'image', batch: 'G11d', group: 'utility_4x4' }),
    mk({ key: 'wheel', batch: 'G11d' }),
    mk({ key: 'utility_4x4_A', kind: 'image', batch: 'G11d', group: 'utility_4x4' }),
    mk({ key: 'utility_4x4_A', kind: 'image', batch: 'G11x', group: 'utility_4x4' }),
  ]
  const tiles = wallTiles(list)
  assert.deepEqual(
    tiles.map((t) => (t.type === 'one' ? t.asset.key : `[${t.assets.map((a) => a.key).join(',')}]`)),
    ['cat', '[utility_4x4_A,utility_4x4_B]', 'wheel', 'utility_4x4_A'],
  )
})

test('并排比较取同组全部候选，不受状态筛选影响', () => {
  const list = [mk({ key: 'm_B', batch: 'G11d', group: 'm', status: 'approved' }), mk({ key: 'm_A', batch: 'G11d', group: 'm' }), mk({ key: 'n_A', batch: 'G11d', group: 'n' })]
  assert.deepEqual(groupMembers(list, 'G11d/m').map((a) => a.key), ['m_A', 'm_B'])
})

test('候选的叫法取组名后面的部分', () => {
  assert.equal(candidateLabel({ key: 'utility_4x4_A', group: 'utility_4x4' }), 'A')
  assert.equal(candidateLabel({ key: 'mule-B2', group: 'mule' }), 'B2')
  assert.equal(candidateLabel({ key: 'solo', group: '' }), 'solo')
})

test('一组的状态标记取最需要处理的那个', () => {
  assert.equal(groupStatus([{ status: 'approved' }, { status: 'pending' }]), 'pending')
  assert.equal(groupStatus([{ status: 'approved' }, { status: 'rework' }]), 'rework')
  assert.equal(groupStatus([{ status: 'approved' }, { status: 'approved' }]), 'approved')
})

test('封面：独立的图用它自己；零件优先特写，其次第一张预览；没有图返回 null', () => {
  const v = (x: object) => ({ id: 'v', version: 1, batch: 'b', branch: '', commit: '', created_at: '', ...x })
  assert.equal(coverOf('image', v({ file: png('cat.png') }))?.name, 'cat.png')
  assert.equal(coverOf('part', v({ file: glb('w.glb'), previews: [png('w_hero.png'), png('w_closeup.png')] }))?.name, 'w_closeup.png')
  assert.equal(coverOf('part', v({ file: glb('w.glb'), previews: [png('w_hero.png')] }))?.name, 'w_hero.png')
  assert.equal(coverOf('part', v({ file: glb('w.glb'), previews: [] })), null)
  assert.equal(glbOf('part', v({ file: glb('w.glb') }))?.name, 'w.glb')
  assert.equal(glbOf('image', v({ file: png('cat.png') })), null)
})

test('版本对比：默认最新版和前一版比；看旧版时和它前一版比；看最早一版时和后一版比；只有一版不比', () => {
  const vs = [{ version: 3 }, { version: 2 }, { version: 1 }]
  assert.deepEqual(comparePair(vs), { before: { version: 2 }, after: { version: 3 } })
  assert.deepEqual(comparePair(vs, 2), { before: { version: 1 }, after: { version: 2 } })
  assert.deepEqual(comparePair(vs, 1), { before: { version: 1 }, after: { version: 2 } })
  assert.equal(comparePair([{ version: 1 }]), null)
})

test('尺寸、面数格式', () => {
  assert.equal(fmtSize([0.3, 0.76, 0.76]), '0.300 × 0.760 × 0.760 m')
  assert.equal(fmtSize(null), '')
  assert.equal(fmtTris(1496, 1500), '1496 / 1500')
  assert.equal(fmtTris(982), '982')
  assert.equal(overBudget(1501, 1500), true)
  assert.equal(overBudget(1500, 1500), false)
})

test('审片队列：只取待审，按图片墙顺序，同组候选展开；下一个跳过已审的并从头绕回', () => {
  const list = [mk({ key: 'a', code: 'A-1' }), mk({ key: 'g_A', code: 'A-2', group: 'g' }), mk({ key: 'b', code: 'A-3', status: 'approved' }), mk({ key: 'g_B', code: 'A-4', group: 'g' })]
  const q = reviewQueue(wallTiles(list))
  assert.deepEqual(q, ['A-1', 'A-2', 'A-4'])
  const st: Record<string, string> = { 'A-1': 'pending', 'A-2': 'approved', 'A-4': 'pending' }
  assert.equal(nextInQueue(q, 0, (c) => st[c]), 2)
  assert.equal(nextInQueue(q, 2, (c) => st[c]), 0)
  st['A-1'] = 'rework'
  assert.equal(nextInQueue(q, 2, (c) => st[c]), null)
})

// R-351 第 4 步：撤销期里先按审核后的状态显示；能下的结论与 Hub 的状态机一致；意见去向的提示；审片模式的前后切换
test('审核：撤销期显示、可下的结论、意见去向、前后切换', () => {
  assert.equal(pendingStatus('pending', 'pass'), 'approved')
  assert.equal(pendingStatus('pending', 'back'), 'rework')
  assert.equal(pendingStatus('pending', undefined), 'pending')
  assert.equal(canReview('pending', 'approved'), true)
  assert.equal(canReview('pending', 'rework'), true)
  assert.equal(canReview('approved', 'approved'), false)
  assert.equal(canReview('approved', 'rework'), true)
  assert.equal(canReview('rework', 'approved'), true)
  assert.equal(canReview('rework', 'rework'), false)
  assert.equal(canReview('wip', 'approved'), false)
  const sessions = [
    { key: 'claude:a', state: 'parked', title: '美术零件' },
    { key: 'claude:b', state: 'ended', title: '旧会话' },
  ]
  assert.deepEqual(recipientOf({ kind: 'session', ref: 'claude:a', name: '推送时的标题' }, sessions), { kind: 'submitter', name: '美术零件' })
  assert.deepEqual(recipientOf({ kind: 'session', ref: 'claude:b' }, sessions), { kind: 'pm' })
  assert.deepEqual(recipientOf({ kind: 'session', ref: 'claude:x' }, sessions), { kind: 'unknown' })
  assert.deepEqual(recipientOf({ kind: 'guest', ref: 'g1', name: '小王' }, sessions), { kind: 'unknown' })
  const q = ['A-1', 'A-2', 'A-3']
  assert.equal(stepQueue(q, 'A-2', 1), 'A-3')
  assert.equal(stepQueue(q, 'A-2', -1), 'A-1')
  assert.equal(stepQueue(q, 'A-3', 1), null)
  assert.equal(stepQueue(q, 'A-1', -1), null)
  assert.equal(stepQueue(q, 'A-9', 1), 'A-1')
})

test('按素材 id 推送的素材批次是多值：分面列出全部批次，按任一批次都筛得到；旧条目仍按 batch（R-376）', () => {
  const byId = mk({ key: 'core:weapon.carbine', uid: 'core:weapon.carbine', batch: '', batches: ['G11e1', 'G12'] })
  const old = mk({ key: 'wheel' })
  assert.deepEqual(batchesOf(byId), ['G11e1', 'G12'])
  assert.deepEqual(batchesOf(old), ['G11e1'])
  assert.deepEqual(batchesOf({ batch: '', batches: [] }), [])
  assert.deepEqual(facetsOf([byId, old], 'p1').batches, ['G11e1', 'G12'])
  const f = { project: 'p1', batch: 'G12', category: '', status: '' }
  assert.deepEqual(filterAssets([byId, old], f).map((a) => a.key), ['core:weapon.carbine'])
  assert.equal(filterAssets([byId, old], { ...f, batch: 'G11e1' }).length, 2)
})

test('预览图按机位分组，机位按出现顺序；旧数据没有机位归在一组（R-376）', () => {
  const ps = [{ id: '1', view: 'front' }, { id: '2', view: 'side' }, { id: '3', view: 'front' }, { id: '4' }]
  assert.deepEqual(
    viewGroups(ps).map((g) => [g.view, g.items.map((x) => x.id)]),
    [['front', ['1', '3']], ['side', ['2']], ['', ['4']]],
  )
})

test('项目字段原样展示，通用字段不重复列出（R-376）', () => {
  assert.deepEqual(metaRows({ lod: 2, faction: 'red', rig: { bones: 12 }, id: 'x', triangles: 3, empty: '' }), [
    ['lod', '2'],
    ['faction', 'red'],
    ['rig', '{"bones":12}'],
  ])
  // budget、size 写成对象时（带项目自己的检查项）照样列出，写成数字 / 数组的已在面数、尺寸里
  assert.deepEqual(metaRows({ budget: { triangles: 784, max_surfaces: 2 }, size: [1, 2, 3] }), [['budget', '{"triangles":784,"max_surfaces":2}']])
  assert.deepEqual(variantNames({ default: {}, v2: {} }), ['default', 'v2'])
  assert.deepEqual(variantNames([{ name: 'desert' }, {}]), ['desert'])
  assert.deepEqual(variantNames(null), [])
  assert.deepEqual(metaRows(null), [])
  assert.deepEqual(metaRows([1, 2]), [])
})

test('封面（R-383）：有预览图用预览图；只有 GLB 的零件用自动生成的缩略图并标明；独立的图不需要', () => {
  const glb: FileLite = { id: 'g', name: 'helmet.glb', mime: 'model/gltf-binary', size: 1 }
  const png = (id: string, name = `${id}.png`): FileLite => ({ id, name, mime: 'image/png', size: 1 })
  const v = (o: object) => ({ id: 'v', version: 1, batch: 'G8', branch: '', commit: '', created_at: '', file: glb, ...o })
  assert.deepEqual(wallCover('part', v({ previews: [png('p')] })), { file: png('p'), auto: false })
  assert.equal(wallCover('part', v({ previews: [] })), null)
  assert.deepEqual(wallCover('part', v({ previews: [], auto_thumb: png('t') })), { file: png('t'), auto: true })
  // 有预览图时不用自动缩略图
  assert.equal(wallCover('part', v({ previews: [png('p')], auto_thumb: png('t') }))?.auto, false)
  assert.equal(needsAutoThumb('part', v({ previews: [] })), true)
  assert.equal(needsAutoThumb('part', v({ previews: [], auto_thumb: png('t') })), false)
  assert.equal(needsAutoThumb('part', v({ previews: [png('p')] })), false)
  assert.equal(needsAutoThumb('part', v({ file: null })), false)
  assert.equal(needsAutoThumb('image', v({ file: png('c') })), false)
})

test('审片进度（R-366）：队列里已审的个数（撤销期里按审核后的状态算），通过 / 返工各几个', () => {
  const st: Record<string, string> = { a: 'approved', b: 'pending', c: 'rework', d: 'pending' }
  assert.deepEqual(reviewProgress(['a', 'b', 'c', 'd'], (c) => st[c]), { done: 2, approved: 1, rework: 1 })
  assert.deepEqual(reviewProgress([], (c) => st[c]), { done: 0, approved: 0, rework: 0 })
})

test('文件类型：取扩展名，只按主文件算，预览图不算，可与其他筛选组合（R-388）', () => {
  assert.equal(fileTypeOf({ name: 'a.glb', mime: 'model/gltf-binary' }), 'GLB')
  assert.equal(fileTypeOf({ name: 'a.jpeg', mime: 'image/jpeg' }), 'JPG')
  assert.equal(fileTypeOf({ name: 'noext', mime: 'audio/ogg' }), 'OGG')
  const f = (name: string, mime: string) => ({ id: name, name, mime, size: 1 })
  const mk = (key: string, status: AssetLite['status'], file: ReturnType<typeof f>, previews: ReturnType<typeof f>[] = []): AssetLite =>
    ({ id: key, code: key, project_id: 'p1', batch: 'B1', key, kind: 'part', category: '车轮', status, version: 1, updated_at: '', latest: { id: key, version: 1, batch: 'B1', branch: '', commit: '', file, previews, created_at: '' } })
  const list = [mk('a', 'pending', f('a.glb', 'model/gltf-binary'), [f('a__front.png', 'image/png')]), mk('b', 'approved', f('b.svg', 'image/svg+xml')), mk('c', 'pending', f('c.ttf', 'font/ttf'))]
  assert.deepEqual(fileTypesOf(list[0]), ['GLB'])
  assert.deepEqual(facetsOf(list, 'p1').fileTypes, ['GLB', 'SVG', 'TTF'])
  const base = { project: 'p1', batch: '', category: '', status: '' }
  assert.deepEqual(filterAssets(list, { ...base, ftype: 'PNG' }).map((a) => a.key), [])
  assert.deepEqual(filterAssets(list, { ...base, ftype: 'GLB' }).map((a) => a.key), ['a'])
  assert.deepEqual(filterAssets(list, { ...base, ftype: 'GLB', status: 'approved' }).map((a) => a.key), [])
  assert.deepEqual(filterAssets(list, { ...base, ftype: 'TTF', status: 'pending', category: '车轮', batch: 'B1' }).map((a) => a.key), ['c'])
})

// ---------- 目录层级（R-394） ----------
const idAsset = (uid: string, sub: string, status: AssetLite['status'] = 'approved', variants?: unknown) => {
  const [, rest] = uid.split(':')
  return mk({ key: uid, uid, batch: '', batches: ['G11d'], category: rest.split('.')[0], status, latest: { id: 'v', version: 1, batch: '', branch: '', commit: '', subtype: sub || undefined, variants, created_at: '' } })
}

test('目录树：类别 → 细类带件数和待审 / 返工数，旧条目单独一栏按批次分', () => {
  const list = [
    idAsset('core:concept.cargo_mule', 'vehicle', 'pending'),
    idAsset('core:concept.hats', 'gear', 'rework'),
    idAsset('core:concept.supply_truck', 'vehicle'),
    idAsset('core:vehicle.wheel', ''),
    mk({ key: 'old1', batch: 'G12', status: 'pending' }),
    mk({ key: 'old2', batch: 'G2', status: 'rework' }),
  ]
  const tr = dirTree(list)
  assert.deepEqual(tr.all, { total: 6, pending: 2, rework: 2 })
  assert.deepEqual(tr.cats.map((c) => [c.key, c.total, c.pending, c.rework]), [['concept', 3, 1, 1], ['vehicle', 1, 0, 0]])
  assert.deepEqual(tr.cats[0].children.map((c) => [c.key, c.total]), [['gear', 1], ['vehicle', 2]])
  assert.deepEqual(tr.cats[1].children.map((c) => c.key), [''])
  assert.deepEqual([tr.old.total, tr.old.children.map((c) => c.key)], [2, ['G2', 'G12']])
  assert.deepEqual(dirCount(tr, { ...ROOT_DIR, cat: 'concept', sub: 'vehicle' }), { key: 'vehicle', total: 2, pending: 1, rework: 0, children: [] })
  assert.deepEqual(dirCount(tr, { ...ROOT_DIR, old: true, batch: 'G12' }).total, 1)
  assert.deepEqual(dirCount(tr, { ...ROOT_DIR, cat: 'nope' }).total, 0)
})

test('点一层筛到这一层；旧条目不进类别，类别里没有旧条目', () => {
  const a = idAsset('core:concept.cargo_mule', 'vehicle')
  const old = mk({ key: 'x', batch: 'G12', category: 'concept' })
  assert.equal(inDir(a, ROOT_DIR) && inDir(old, ROOT_DIR), true)
  assert.equal(inDir(a, { ...ROOT_DIR, cat: 'concept' }), true)
  assert.equal(inDir(old, { ...ROOT_DIR, cat: 'concept' }), false)
  assert.equal(inDir(a, { ...ROOT_DIR, cat: 'concept', sub: 'gear' }), false)
  assert.equal(inDir(old, { ...ROOT_DIR, old: true, batch: 'G12' }), true)
  assert.equal(inDir(a, { ...ROOT_DIR, old: true }), false)
})

test('图片墙按下一层分组：全部按类别（旧条目按批次排在后面）、类别按细类（未分细类在最后）、细类不分组', () => {
  const list = [idAsset('core:concept.b', 'vehicle'), idAsset('core:concept.a', ''), idAsset('core:audio.c', 'sound'), mk({ key: 'o', batch: 'G9' })]
  assert.deepEqual(wallSections(list, ROOT_DIR).map((s) => [s.kind, s.key, s.assets.length]), [['cat', 'audio', 1], ['cat', 'concept', 2], ['batch', 'G9', 1]])
  const concept = list.filter((a) => inDir(a, { ...ROOT_DIR, cat: 'concept' }))
  assert.deepEqual(wallSections(concept, { ...ROOT_DIR, cat: 'concept' }).map((s) => [s.kind, s.key]), [['sub', 'vehicle'], ['sub', '']])
  assert.deepEqual(wallSections(concept.slice(1), { ...ROOT_DIR, cat: 'concept' }).map((s) => s.kind), [''])
  assert.deepEqual(wallSections(concept, { ...ROOT_DIR, cat: 'concept', sub: 'vehicle' }).map((s) => s.kind), [''])
  assert.deepEqual(wallSections([list[3]], { ...ROOT_DIR, old: true }).map((s) => [s.kind, s.key]), [['batch', 'G9']])
})

test('素材名去掉命名空间和类别，变体按 asset.json 数', () => {
  assert.equal(assetName({ uid: 'core:weapon.carbine', key: 'core:weapon.carbine' }), 'carbine')
  assert.equal(assetName({ uid: 'core:ui.icons.reload', key: '' }), 'icons.reload')
  assert.equal(assetName({ uid: '', key: 'wheel_A' }), 'wheel_A')
  assert.equal(variantCount(idAsset('core:concept.a', '', 'approved', { a: {}, b: {}, c: {} })), 3)
  assert.equal(variantCount(idAsset('core:concept.a', '')), 0)
  assert.equal(subtypeOf(idAsset('core:concept.a', 'gear')), 'gear')
})

test('类型标签（R-397）：按文件类型分种类，写成「种类 · 文件类型」用的种类和类型', () => {
  assert.equal(mediaKindOf('GLB'), 'model')
  assert.equal(mediaKindOf('fbx'), 'model')
  assert.equal(mediaKindOf('PNG'), 'image')
  assert.equal(mediaKindOf('JPG'), 'image')
  assert.equal(mediaKindOf('OGG'), 'audio')
  assert.equal(mediaKindOf('MP4'), 'video')
  assert.equal(mediaKindOf('XYZ', 'image/x-foo'), 'image')
  assert.equal(mediaKindOf('TTF', 'font/ttf'), 'other')
  assert.equal(mediaKindOf(''), 'other')
  const f = (name: string, mime: string) => ({ id: name, name, mime, size: 1 })
  const lat = (file: ReturnType<typeof f> | null, previews: ReturnType<typeof f>[] = []) => ({ latest: { id: 'v', version: 1, batch: '', branch: '', commit: '', file, previews, created_at: '' } })
  assert.deepEqual(fileTagsOf(lat(f('a.glb', 'model/gltf-binary'), [f('a__front.png', 'image/png')])), [{ kind: 'model', type: 'GLB' }])
  assert.deepEqual(fileTagsOf(lat(f('b.jpeg', 'image/jpeg'))), [{ kind: 'image', type: 'JPG' }])
  assert.deepEqual(fileTagsOf(lat(null)), [])
  assert.deepEqual(fileTagsOf({ latest: null }), [])
  assert.deepEqual(uniqTags([{ kind: 'image', type: 'PNG' }, { kind: 'model', type: 'GLB' }, { kind: 'image', type: 'PNG' }]), [{ kind: 'image', type: 'PNG' }, { kind: 'model', type: 'GLB' }])
})

test('详情默认视图（R-395）：有 GLB 的零件先看 3D，3D 排在切换的最前；只有图片的照旧看大图', () => {
  const f = (name: string, mime: string) => ({ id: name, name, mime, size: 1 })
  const v = (file: ReturnType<typeof f> | null, previews: ReturnType<typeof f>[] = []) => ({ id: 'v', version: 1, batch: '', branch: '', commit: '', file, previews, created_at: '' })
  assert.equal(defaultView('part', v(f('a.glb', 'model/gltf-binary'), [f('a__front.png', 'image/png')])), 'model')
  assert.equal(defaultView('part', v(f('a.glb', 'model/gltf-binary'))), 'model')
  assert.equal(defaultView('image', v(f('c.png', 'image/png'))), 'image')
  assert.equal(defaultView('part', v(null, [f('a.png', 'image/png')])), 'image')
  assert.equal(defaultView('part', null), 'image')
  assert.deepEqual(detailViews({ glb: true, pair: true, pair3d: true }), ['model', 'image', 'compare', 'compare3d'])
  assert.deepEqual(detailViews({ glb: false, pair: true, pair3d: false }), ['image', 'compare'])
  assert.deepEqual(detailViews({ glb: false, pair: false, pair3d: false }), ['image'])
})

test('宽屏分栏（R-396）：1920px 起分栏，详情栏宽度 560～720px', () => {
  assert.equal(SPLIT_MIN, 1920)
  assert.equal(splitWidth(1920), 560)
  assert.equal(splitWidth(2560), 666)
  assert.equal(splitWidth(3440), 720)
  assert.equal(splitWidth(5120), 720)
  assert.equal(splitWidthCss, 'clamp(560px, 26vw, 720px)')
})
