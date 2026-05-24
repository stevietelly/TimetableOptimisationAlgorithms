# How the Genetic Algorithm Timetable Solver Works

The goal is simple: assign every lesson a day, time, and room such that nothing clashes and everyone's preferences are respected. The search space is enormous — thousands of possible combinations — so instead of trying them all, we evolve a solution.

---

## The Core Idea

Think of it like selective breeding. You start with a population of rough draft schedules, score them, keep the best ones, and combine their good parts to produce better drafts. Repeat until you have a schedule good enough to publish.

---

## Step 1 — Build a Population of Draft Schedules

A single draft schedule is called a **chromosome**. It is just an array where each entry (a **gene**) says: *"Lesson X happens on Day Y at Time Z in Room R."*

We create 80 of these draft schedules to start with. Rather than picking days and times randomly, we use a **smart initialiser** that checks what slots are already taken for each instructor, group, and room before making an assignment. This means our very first drafts are already 60–80% conflict-free.

---

## Step 2 — Score Each Draft

Every draft schedule is run through an **evaluator** that produces a score from 0 to 100.

The score is a weighted combination of four things:

| What is checked | Weight |
|---|---|
| Hard constraints (no double-bookings) | 60% |
| Preferences (e.g. a class must be on Friday morning) | 20% |
| Lesson distribution (don't stack all sessions on one day) | 10% |
| Preferred rooms (groups get their usual venues) | 10% |

A score of 85 or above is considered a healthy, publishable schedule. Below 60 means there are still hard clashes — do not use it.

---

## Step 3 — Fix the Clashes (Phase 1)

Before mixing drafts together, we first repair any hard clashes. The evaluator tells us exactly what went wrong — instructor double-booked on Tuesday at 9am, two groups in the same room on Wednesday at 2pm — and we move the offending lesson to a free slot.

This targeted repair is far faster than hoping random changes stumble upon a fix.

We keep doing this for up to 20 generations. Once all hard clashes are gone, we move to Phase 2.

---

## Step 4 — Breed Better Schedules (Phase 2)

Now that every draft in the population is clash-free, we start combining them.

**Selection** — Pick five drafts at random, keep the best one. Do this twice to get two parents.

**Crossover** — Cut both parents at two random points and splice them together. The child inherits the first parent's lessons outside the cut, and the second parent's lessons inside the cut. Good blocks of well-scheduled lessons are passed on.

```
Parent A:  [ A1  A2  A3 | B4  B5  B6 | A7  A8 ]
Parent B:  [ B1  B2  B3 | B4  B5  B6 | B7  B8 ]
                         ↑ cut                ↑ cut

Child:     [ A1  A2  A3   B4  B5  B6   A7  A8 ]
```

**Mutation** — After crossover, we apply two kinds of change:
- **Targeted**: if the evaluator flagged a preference violation, move that lesson to a slot that satisfies it.
- **Random**: nudge 5% of lessons to random new slots, so the population doesn't get stuck in a local optimum.

---

## Step 5 — Repeat Until Done

Each round of scoring, selection, crossover, and mutation is one **generation**. We run up to 200 generations, but usually stop early when:

- The overall score stops improving for 30 generations in a row, or
- A perfect hard-constraint score (100/100) is reached.

The best chromosome at the end is converted directly into the `sessions[]` output and returned.

---

## Why Two Phases?

Crossover between two schedules that both have hard clashes will inherit those clashes in the child. By spending Phase 1 eliminating all clashes first, Phase 2 crossover only combines clash-free material — so the population never regresses.

---

## Special Case — Merged Lessons

Some lessons (like HIV/AIDS and Society) are taught to eight student groups simultaneously. These are encoded as a **single gene** with one day and time slot. The evaluator checks all eight groups against that one slot. This prevents the solver from ever accidentally splitting a merged lecture across multiple times.

---

## Summary

```
Start: 80 smart-initialised draft schedules
  ↓
Phase 1: repair hard clashes with targeted mutation (≤ 20 generations)
  ↓
Phase 2: crossover + mutation to optimise soft constraints (≤ 180 generations)
  ↓
Stop: score plateaus for 30 gens OR perfect hard score reached
  ↓
Output: best schedule as sessions[]
```

The whole process typically completes in under 5 seconds.