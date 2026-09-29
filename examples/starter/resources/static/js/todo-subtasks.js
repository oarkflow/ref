// Progressive-enhancement widget for a todo's checklist: Add/Remove rows and
// reorder them with Up/Down, then save the whole list in one request.
//
// This is JS-only, deliberately: reordering rows before submit has no
// meaningful non-JS equivalent, and the wire format it saves
// (`{"subtasks":[{"title":"..."}, ...]}`) is a JSON body a native
// application/x-www-form-urlencoded <form> post can't produce at all — see
// resources/config/12_todo_workflow_example.bcl's todo.subtasks.replace
// comment for why that's a real platform limit, not just a choice here.
// Without JS the rows server-rendered into #subtask-rows still show; there's
// just no way to change them, which is what the <noscript> hint next to
// them says.
(function () {
  "use strict";

  var panel = document.getElementById("subtasks-panel");
  if (!panel) return;

  var todoId = panel.getAttribute("data-todo-id");
  var rows = document.getElementById("subtask-rows");
  var status = document.getElementById("subtask-status");
  var addButton = document.getElementById("subtask-add");
  var saveButton = document.getElementById("subtask-save");

  function makeRow(title) {
    var li = document.createElement("li");
    li.className = "subtask-row";

    var input = document.createElement("input");
    input.type = "text";
    input.className = "form-control subtask-title";
    input.placeholder = "Checklist item";
    input.value = title || "";
    li.appendChild(input);

    var actions = document.createElement("span");
    actions.className = "subtask-actions";

    [
      ["↑", "subtask-up", "Move up"],
      ["↓", "subtask-down", "Move down"],
      ["Remove", "subtask-remove", "Remove"],
    ].forEach(function (spec) {
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn btn-sm btn-secondary " + spec[1];
      btn.textContent = spec[0];
      btn.setAttribute("aria-label", spec[2]);
      actions.appendChild(btn);
    });
    li.appendChild(actions);

    return li;
  }

  addButton.addEventListener("click", function () {
    var row = makeRow("");
    rows.appendChild(row);
    row.querySelector(".subtask-title").focus();
  });

  rows.addEventListener("click", function (event) {
    var button = event.target.closest("button");
    if (!button) return;
    var row = button.closest(".subtask-row");
    if (!row) return;

    if (button.classList.contains("subtask-remove")) {
      row.remove();
    } else if (button.classList.contains("subtask-up")) {
      var prev = row.previousElementSibling;
      if (prev) rows.insertBefore(row, prev);
    } else if (button.classList.contains("subtask-down")) {
      var next = row.nextElementSibling;
      if (next) rows.insertBefore(next, row);
    }
  });

  saveButton.addEventListener("click", function () {
    var titles = Array.prototype.map.call(
      rows.querySelectorAll(".subtask-title"),
      function (input) {
        return input.value;
      }
    );
    var subtasks = titles
      .filter(function (title) {
        return title.trim() !== "";
      })
      .map(function (title) {
        return { title: title };
      });

    status.textContent = "Saving…";
    saveButton.disabled = true;

    fetch("/api/v1/todos/" + todoId + "/subtasks", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ subtasks: subtasks }),
    })
      .then(function (response) {
        if (!response.ok) throw new Error("save failed");
        return response.json();
      })
      .then(function () {
        status.textContent = "Checklist saved.";
      })
      .catch(function () {
        status.textContent = "Could not save the checklist — try again.";
      })
      .finally(function () {
        saveButton.disabled = false;
      });
  });
})();
