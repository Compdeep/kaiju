# Deep web research

When a request asks for real sources, market figures, or evidence from the web,
don't stop at the first page. Work the problem:

1. **Search broadly, then narrow.** Run several searches with different angles
   (by vendor, by figure, by primary-source type: `... research paper`,
   `... government report`, `... whitepaper pdf`). One query rarely surfaces
   everything.
2. **Prefer primary sources over aggregators.** A number from a government
   agency, a peer-reviewed paper, or an original vendor report is worth more than
   the same number relayed by an aggregator. When the user asks for
   non-aggregator sources, treat aggregator hits as leads to the primary source,
   not as the answer.
3. **Use `web_read`, not a plain fetch, for real pages.** `web_read` renders
   JavaScript and strips boilerplate, so it reads analyst pages, docs, and
   reports that a plain fetch returns empty on. If it comes back `empty`, the page
   is genuinely unreadable (login-walled or image-only) — move to the next result
   rather than guessing its contents.
4. **Keep going until you hit the target.** If the ask is "at least 10 sources,"
   count what you actually retrieved. If a read fails, try the next result — a
   failed fetch is a reason to continue, not to conclude.
5. **Report what you have, and name the gaps.** Give the sources and figures you
   genuinely found, labelled by where they came from, and say plainly which parts
   you could not source. Never invent a citation, link, figure, or date to fill a
   gap — an honest gap is the correct answer.
