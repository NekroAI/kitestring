import { useEffect, useState } from 'react'
import { loadPref, savePref } from './util'

export type ThemePref = 'system' | 'light' | 'dark'
const KEY = 'ks.theme'

function apply(pref: ThemePref) {
  const dark = pref === 'dark' || (pref === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', dark ? '#1c2232' : '#f4f0e6')
}

// initTheme 在渲染前调用，避免闪烁。
export function initTheme() {
  apply(loadPref<ThemePref>(KEY, 'system'))
}

export function useTheme(): [ThemePref, (p: ThemePref) => void] {
  const [pref, setPref] = useState<ThemePref>(() => loadPref<ThemePref>(KEY, 'system'))
  useEffect(() => {
    apply(pref)
    if (pref !== 'system') return
    const m = window.matchMedia('(prefers-color-scheme: dark)')
    const on = () => apply('system')
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [pref])
  return [
    pref,
    (p) => {
      savePref(KEY, p)
      setPref(p)
    },
  ]
}
