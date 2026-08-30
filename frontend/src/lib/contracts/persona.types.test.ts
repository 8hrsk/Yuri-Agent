import { fileURLToPath } from 'node:url'

import ts from 'typescript'
import { describe, expect, it } from 'vitest'

import type { AffectEmotion, PersonaTraitId } from './persona'

/**
 * N-19. `'a' | 'b' | string` is not an open union: `string` is a supertype of
 * every string literal, so TypeScript reduces the whole alias to `string` and
 * the listed members vanish — no autocomplete, no documentation, no way to see
 * the intended vocabulary in an editor. It looks correct and does nothing.
 *
 * The guard has to be the compiler's own opinion rather than the source text,
 * because the source text is exactly what looks fine to a reader. These tests
 * ask the checker what the alias actually resolved to.
 */

const personaFile = fileURLToPath(new URL('./persona.ts', import.meta.url))

const program = ts.createProgram([personaFile], {
  noEmit: true,
  strict: true,
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
})
const checker = program.getTypeChecker()
const source = program.getSourceFile(personaFile)

function aliasType(name: string): ts.Type {
  if (!source) throw new Error(`persona.ts is not in the program`)
  for (const statement of source.statements) {
    if (ts.isTypeAliasDeclaration(statement) && statement.name.text === name) {
      return checker.getTypeAtLocation(statement.name)
    }
  }
  throw new Error(`no type alias named ${name}`)
}

/** Literal members the alias still carries, in source order. */
function literalMembers(name: string): string[] {
  const type = aliasType(name)
  if (!type.isUnion()) return []
  return type.types.filter((member) => member.isStringLiteral()).map((member) => member.value)
}

describe('persona id unions stay open without collapsing to string (N-19)', () => {
  it('keeps PersonaTraitId a union rather than reducing it to string', () => {
    const type = aliasType('PersonaTraitId')

    // The collapse is visible in exactly this way: the alias prints as `string`
    // and is not a union at all.
    expect(checker.typeToString(type)).not.toBe('string')
    expect(type.isUnion()).toBe(true)
    expect(literalMembers('PersonaTraitId')).toEqual([
      'warmth',
      'trust',
      'attachment',
      'jealousy',
      'irritability',
      'romantic_tone',
      'emotionality',
      'directness',
      'playfulness',
      'formality',
      'initiative',
      'empathy',
      'sociability',
      'shyness',
      'anxiety',
      'fearfulness',
      'emotional_stability',
      'sensitivity',
      'possessiveness',
      'impulsivity',
      'stubbornness',
      'optimism',
      'curiosity',
      'suspicion',
      'tsundere',
    ])
  })

  it('keeps AffectEmotion a union rather than reducing it to string', () => {
    const type = aliasType('AffectEmotion')

    expect(checker.typeToString(type)).not.toBe('string')
    expect(type.isUnion()).toBe(true)
    expect(literalMembers('AffectEmotion').sort()).toEqual([
      'anger',
      'anxiety',
      'boredom',
      'gratitude',
      'irritation',
      'jealousy',
      'joy',
      'resentment',
      'sympathy',
      'tenderness',
    ])
  })

  it('still admits ids the renderer has never heard of', () => {
    // Both vocabularies are open by contract: `domain.CommonPersonaTraits` is
    // "the stable set used by the default seed" with custom snake_case names
    // allowed, and `internal/domain/affect.go` says emotion names are
    // extensible. These four assignments are the actual regression guard —
    // `tsc` rejects the file if the open half is ever dropped, and rejects the
    // last two if the alias stops widening to `string`.
    const seeded: PersonaTraitId = 'tsundere'
    const invented: PersonaTraitId = 'speech_habit_pauses'
    const emotion: AffectEmotion = 'longing'
    const widened: string = seeded

    expect([seeded, invented, emotion, widened]).toEqual([
      'tsundere',
      'speech_habit_pauses',
      'longing',
      'tsundere',
    ])
  })
})
