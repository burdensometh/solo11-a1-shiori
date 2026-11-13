// Archive manifest banner: shows how many resources of the archived page are
// missing according to its resource manifest, and allows retrying them.
(function () {
	var container = document.getElementById("archive-manifest");
	if (!container) return;

	var bookmarkID = container.getAttribute("data-bookmark-id");
	if (!bookmarkID) return;

	var apiURL = new URL(
		"api/v1/bookmarks/" + encodeURIComponent(bookmarkID) + "/archive/manifest",
		document.baseURI,
	);
	var iframe = document.querySelector("iframe");
	var currentManifest = null;

	function createButton(label) {
		var button = document.createElement("button");
		button.type = "button";
		button.textContent = label;
		return button;
	}

	function setButtonsDisabled(disabled) {
		var buttons = container.querySelectorAll("button");
		for (var i = 0; i < buttons.length; i++) {
			buttons[i].disabled = disabled;
		}
	}

	function render(manifest) {
		container.textContent = "";
		container.classList.remove("manifest-warning", "manifest-ok");

		if (manifest.missing > 0) {
			container.classList.add("manifest-warning");

			var summary = document.createElement("span");
			summary.className = "manifest-summary";
			summary.textContent =
				"Archive incomplete: " +
				manifest.missing +
				" of " +
				manifest.total +
				" resources missing.";
			container.appendChild(summary);

			var retryAllButton = createButton("Retry all");
			retryAllButton.addEventListener("click", function () {
				retry([]);
			});
			container.appendChild(retryAllButton);

			var list = document.createElement("ul");
			list.className = "manifest-list";
			list.hidden = true;

			var toggleButton = createButton("Details");
			toggleButton.addEventListener("click", function () {
				list.hidden = !list.hidden;
				toggleButton.textContent = list.hidden ? "Details" : "Hide details";
			});
			container.appendChild(toggleButton);

			manifest.resources.forEach(function (resource) {
				if (resource.status !== "failed") return;

				var item = document.createElement("li");

				var type = document.createElement("span");
				type.className = "manifest-type";
				type.textContent = resource.type;
				item.appendChild(type);

				var url = document.createElement("span");
				url.className = "manifest-url";
				url.textContent = resource.url;
				url.title = resource.error
					? resource.url + "\n" + resource.error
					: resource.url;
				item.appendChild(url);

				var retryButton = createButton("Retry");
				retryButton.addEventListener("click", function () {
					retry([resource.url]);
				});
				item.appendChild(retryButton);

				list.appendChild(item);
			});

			container.appendChild(list);
		} else {
			container.classList.add("manifest-ok");

			var okText = document.createElement("span");
			okText.className = "manifest-summary";
			okText.textContent =
				"Archive complete: " + manifest.total + " resources saved.";
			container.appendChild(okText);

			var dismissButton = createButton("×");
			dismissButton.title = "Dismiss";
			dismissButton.addEventListener("click", function () {
				container.hidden = true;
			});
			container.appendChild(dismissButton);
		}
	}

	function showError(message) {
		var error = document.createElement("span");
		error.className = "manifest-error";
		error.textContent = message;
		container.appendChild(error);
		setTimeout(function () {
			error.remove();
		}, 5000);
	}

	function retry(urls) {
		setButtonsDisabled(true);
		fetch(apiURL.toString() + "/retry", {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ urls: urls }),
		})
			.then(function (response) {
				if (!response.ok)
					throw new Error("retry request failed: " + response.status);
				return response.json();
			})
			.then(function (manifest) {
				var improved =
					currentManifest !== null &&
					manifest.missing < currentManifest.missing;
				currentManifest = manifest;
				render(manifest);
				// Reload the archived page so recovered resources become visible
				if (improved && iframe && iframe.contentWindow) {
					iframe.contentWindow.location.reload();
				}
			})
			.catch(function () {
				showError("Retry failed. Check the server logs for details.");
			})
			.finally(function () {
				setButtonsDisabled(false);
			});
	}

	fetch(apiURL)
		.then(function (response) {
			if (!response.ok)
				throw new Error("manifest request failed: " + response.status);
			return response.json();
		})
		.then(function (manifest) {
			currentManifest = manifest;
			container.hidden = false;
			render(manifest);
		})
		.catch(function () {
			// No manifest available (old archive or not logged in): keep the banner hidden
			container.hidden = true;
		});
})();
