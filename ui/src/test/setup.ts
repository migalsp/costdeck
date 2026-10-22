import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// jsdom has no matchMedia; the theme module reads it when it loads. Tests that care
// replace it with their own before importing the module.
if (!window.matchMedia) {
  window.matchMedia = (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })
}

afterEach(() => {
  cleanup()
  localStorage.clear()
  document.documentElement.className = ''
})

// jsdom does not lay anything out, so it has no scrolling methods.
Element.prototype.scrollTo = function () {}
Element.prototype.scrollIntoView = function () {}
