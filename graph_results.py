import matplotlib.pyplot as plt
import json
import numpy as np


def slice_array(arr):
    return np.concatenate([arr[:100000], arr[100000::10000]])


def graph_contracts_storage(log: bool):
    storage_sizes = np.load("storage_sizes.pickle", allow_pickle=True)
    storage_sizes.sort(descending=True)
    storage_sizes_cumsum = np.cumsum(storage_sizes)
    print(f"Total storage size {storage_sizes_cumsum[-1]}")
    storage_sizes_cumsum = storage_sizes_cumsum / storage_sizes_cumsum[-1] * 100
    num_contracts = np.array(range(1, len(storage_sizes) + 1)) / len(storage_sizes) * 100
    
    plt.plot(slice_array(num_contracts), slice_array(storage_sizes_cumsum), marker='o', markersize=3)
    
    plt.title(f"Storage Size Distribution of Contracts{' (log scale)' if log else ''}")
    plt.xlabel("Percentage of Contracts")
    plt.ylabel("Percentage of Storage")
    plt.ylim(0, 100)
    if log:
        plt.xscale('log')
    else:
        plt.xlim(0, 100)
    plt.tight_layout()
    plt.savefig(f"storage_size_distribution{'_log' if log else ''}.svg")
    plt.clf()
    
    
def graph_big_contracts():
    with open("big_contracts.json") as f:
        big_contracts = json.load(f)
        sizes = [contract["numSlots"] for contract in big_contracts]
        labels = [contract["address"] for contract in big_contracts]
        leftover = 1625282405 - sum(sizes)
        sizes.append(leftover)
        labels.append("Rest")
        # sort by sizes
        sizes, labels = zip(*sorted(zip(sizes, labels), reverse=True))
        new_labels = [""] * len(labels)
        new_labels[0] = labels[0]
        new_labels[1] = labels[1]
        plt.pie(sizes, labels=new_labels)
        plt.title("Composition of Ethereum Contract storage")
        plt.tight_layout()
        plt.savefig("big_contracts_pie.svg")

    
if __name__ == "__main__":
    # graph_contracts_storage(True)
    # graph_contracts_storage(False)
    graph_big_contracts()