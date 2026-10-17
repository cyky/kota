/**
 * kota's ticket_done tool for pi. The agent calls it after committing a
 * finished ticket. When that turn ends, the conversation is replaced by a
 * fixed note that points to the tracker and the commits, so the next ticket
 * starts from a near-empty context in the same session.
 *
 * The note is written here, not by the model: a compaction entry chained
 * from turn_end with a null firstKeptEntryId, which keeps none of the
 * earlier entries. ctx.compact() isn't used, because it aborts the running
 * turn, which would stop an unattended agent after every ticket. Automatic
 * and /compact compactions are left to pi.
 *
 * kota mounts this directory read-only. Edits to this file take effect in
 * the next session.
 */
import { posix } from "node:path";

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const WORKSPACE = "/workspace";

// checkTicket returns why ticket isn't a ticket number or a path under
// WORKSPACE, or undefined when it is one.
function checkTicket(ticket: string): string | undefined {
	if (/^#?\d+$/.test(ticket)) {
		return undefined;
	}
	const full = posix.resolve(WORKSPACE, ticket);
	if (!full.startsWith(WORKSPACE + "/")) {
		return `ticket must be a ticket number or a path under ${WORKSPACE}, not ${JSON.stringify(ticket)}`;
	}
	return undefined;
}

function summary(ticket: string, commit: string): string {
	return [
		`Ticket ${ticket} is done in commit ${commit}. The previous conversation was cleared.`,
		"Your state is in the project's tickets and git history, not here.",
		"Re-read the project's AGENTS.md and the tracker, then take the next ticket from the frontier.",
		"If the user told you to work unattended, that still applies.",
	].join("\n");
}

export default function ticketDone(pi: ExtensionAPI) {
	// The finished ticket of a successful call in the current turn.
	let done: { ticket: string; commit: string } | undefined;

	pi.registerTool({
		name: "ticket_done",
		label: "Ticket done",
		description:
			"Clear your context after you have committed a finished ticket. When this turn ends, the conversation " +
			"so far is replaced by a short note naming the ticket and commit, and you continue with the next " +
			"ticket. Call it only after the ticket is marked done and committed.",
		parameters: Type.Object({
			ticket: Type.String({ description: `The finished ticket: its path under ${WORKSPACE}, or its number` }),
			commit: Type.String({ description: "The commit that finished it" }),
		}),
		async execute(_toolCallId, params) {
			const ticket = params.ticket.trim();
			const commit = params.commit.trim();
			if (!ticket || !commit) {
				throw new Error("ticket and commit are both required");
			}
			const problem = checkTicket(ticket);
			if (problem) {
				throw new Error(problem);
			}
			done = { ticket, commit };
			return {
				content: [{ type: "text", text: "The conversation will be cleared when this turn ends." }],
				details: undefined,
			};
		},
	});

	pi.on("turn_end", (event, ctx) => {
		const finished = done;
		done = undefined;
		// An aborted or failed turn isn't continued: the user stopped it, or
		// something went wrong, and the agent can call the tool again.
		if (!finished || event.outcome !== "completed") {
			return;
		}
		if (ctx.hasUI) {
			ctx.ui.notify(`ticket_done: context cleared after ticket ${finished.ticket}`, "info");
		}
		return {
			entries: [
				{
					type: "compaction",
					summary: summary(finished.ticket, finished.commit),
					firstKeptEntryId: null,
					details: { tool: "ticket_done", ...finished },
				},
			],
			continue: true,
		};
	});
}
