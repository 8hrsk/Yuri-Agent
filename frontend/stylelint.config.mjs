/**
 * Tiered adoption, mirroring .golangci.yml: the blocking set below is green
 * today, so the CI step is meaningful from the first run instead of being
 * permanently red and ignored.
 *
 * Blocking = every rule in stylelint-config-standard except the ones listed
 * under "staged" below. `no-duplicate-selectors` is the rule this config exists
 * for (finding M-47) and is re-stated explicitly so it cannot be turned off by
 * accident.
 *
 * Staged for later enablement, with the current issue count and what has to
 * happen first:
 *
 *   selector-class-pattern      985 issues — the sheet is BEM-ish (`block__elem--mod`)
 *                                            but not consistently; enabling this means
 *                                            renaming classes, which means touching every
 *                                            component. Needs its own wave, not a CSS pass.
 *   alpha-value-notation        390 issues — `rgba(x, y, z, 0.5)` -> `50%`. Mechanical, but
 *                                            it rewrites 390 colour literals, which would
 *                                            collide head-on with the tokenisation work.
 *                                            Do it after tokenisation settles.
 *   color-function-notation     390 issues — same literals, comma -> space syntax. Same
 *                                            reason to defer; do it in one pass with
 *                                            alpha-value-notation.
 *   font-family-name-quotes     116 issues — `'SFMono-Regular'` etc. quoted where the rule
 *                                            wants them bare. Mechanical and safe, but
 *                                            font-stack edits are the one class of change
 *                                            that silently alters metrics if a name is
 *                                            mistyped; wants a careful pass.
 *   no-descending-specificity    69 issues — real cascade smells, but every fix moves a
 *                                            rule, and moving a rule is exactly the change
 *                                            this effort proved is unsafe without an
 *                                            equivalence check. Fix with the checker in
 *                                            hand, a few at a time.
 *   declaration-block-single-line-max-declarations
 *                                25 issues — all inside @keyframes (`from { a: 1; b: 2 }`).
 *                                            Purely cosmetic.
 *   import-notation              17 issues — the barrel's `@import "./x.css"`. Switching to
 *                                            `url()` changes how Vite resolves them; not
 *                                            worth the risk for a style preference.
 *   media-feature-range-notation 17 issues — `(max-width: 900px)` -> `(width <= 900px)`.
 *                                            Range syntax is newer than the browser floor
 *                                            this app has been tested against.
 */
export default {
  extends: ['stylelint-config-standard'],
  rules: {
    'no-duplicate-selectors': true,

    // staged — see the counts above
    'selector-class-pattern': null,
    'alpha-value-notation': null,
    'color-function-notation': null,
    'font-family-name-quotes': null,
    'no-descending-specificity': null,
    'declaration-block-single-line-max-declarations': null,
    'import-notation': null,
    'media-feature-range-notation': null,
  },
}
